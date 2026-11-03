package transport

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// RFC6455 里写死的握手魔术串：accept = base64(sha1(key + 这一串))。
const wsAcceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsMaxPayload 是单帧正文的上限。读和写都卡这条线：
// 没有上限的读等于把内存交给对端，没有上限的写等于把对端的内存当自己家的。
const wsMaxPayload = 1 << 20

const (
	wsOpText  byte = 0x1
	wsOpClose byte = 0x8
	wsOpPing  byte = 0x9
	wsOpPong  byte = 0xA
)

// WSConn 是 Conn 的 WebSocket 实现（最小子集：文本帧、无扩展、无压缩）。
//
// 读走 br（Hijack 时 http 层可能已经缓冲了几个字节，必须接着那个 reader 读，
// 否则会丢掉握手后紧跟的第一帧）。写走 conn，并用 wmu 串行化：
// 一条连接上写帧必须是一个整体，不能被另一个 goroutine 插进半个头。
type WSConn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
}

// 钉住契约：WSConn 就是 ServeConn / Hub 要的那种 Conn。
var _ Conn = (*WSConn)(nil)

// Upgrade 完成 RFC6455 握手，并把这条 TCP 连接包成 WSConn。
//
// 缺任何一项握手要素都会先回 400 再返回 error——此时连接还归 http 服务器，
// 不能用 Hijack 之后的裸写。
func Upgrade(w http.ResponseWriter, r *http.Request) (*WSConn, error) {
	if r == nil {
		http.Error(w, "WebSocket 握手需要请求对象", http.StatusBadRequest)
		return nil, errors.New("升级失败：请求为 nil")
	}
	if !headerHasToken(r.Header, "Upgrade", "websocket") {
		http.Error(w, "缺少 Upgrade: websocket", http.StatusBadRequest)
		return nil, errors.New("升级失败：请求头缺少 Upgrade: websocket")
	}
	if !headerHasToken(r.Header, "Connection", "upgrade") {
		http.Error(w, "缺少 Connection: Upgrade", http.StatusBadRequest)
		return nil, errors.New("升级失败：请求头 Connection 不含 upgrade")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		http.Error(w, "缺少 Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, errors.New("升级失败：请求头缺少 Sec-WebSocket-Key")
	}

	h, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "这个 http.ResponseWriter 不支持 Hijack", http.StatusInternalServerError)
		return nil, errors.New("升级失败：http.ResponseWriter 不支持 Hijack，无法接管连接")
	}
	conn, brw, err := h.Hijack()
	if err != nil {
		return nil, fmt.Errorf("升级失败：接管连接出错：%w", err)
	}
	// Hijack 之后 http 层不再管这条连接，任何失败路径都要自己关掉它。
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("升级失败：回 101 时写不出去：%w", err)
	}
	return &WSConn{conn: conn, br: brw.Reader}, nil
}

// headerHasToken 判断某个头里是否含指定 token（大小写不敏感，且按逗号拆开）。
// Upgrade 一般是单个 token，Connection 常见 "keep-alive, Upgrade"，所以必须拆。
func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// acceptKey 算 Sec-WebSocket-Accept。
func acceptKey(key string) string {
	h := sha1.New()
	io.WriteString(h, key)
	io.WriteString(h, wsAcceptGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// ReadClientFrame 读一个文本帧，JSON 解成 ClientFrame。
// 对端正常关闭（opcode 8）会回一个 close 帧，然后返回 io.EOF。
func (c *WSConn) ReadClientFrame() (ClientFrame, error) {
	for {
		op, payload, err := c.readFrame()
		if err != nil {
			return ClientFrame{}, err
		}
		switch op {
		case wsOpText:
			var f ClientFrame
			if err := json.Unmarshal(payload, &f); err != nil {
				return ClientFrame{}, fmt.Errorf("文本帧正文不是合法 JSON（%v）：ws 上收发的是 ClientFrame 的 JSON，请检查对端序列化", err)
			}
			return f, nil
		case wsOpClose:
			// 回一个 close（正文原样带回去，里面有对方给的状态码）。
			// 回不出来也不能把 io.EOF 变成别的错误：对端已经走了。
			_ = c.writeFrame(wsOpClose, payload)
			return ClientFrame{}, io.EOF
		case wsOpPing:
			if err := c.writeFrame(wsOpPong, payload); err != nil {
				return ClientFrame{}, fmt.Errorf("收到 ping 但回 pong 失败：%w", err)
			}
			continue
		default:
			return ClientFrame{}, fmt.Errorf("不支持的 opcode 0x%x：本实现只处理 text(1)/close(8)/ping(9)", op)
		}
	}
}

// WriteServerFrame 写一个文本帧，内容是 ServerFrame 的 JSON。
func (c *WSConn) WriteServerFrame(f ServerFrame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("ServerFrame 序列化失败：%w", err)
	}
	return c.writeFrame(wsOpText, b)
}

// Close 关掉底层连接。Hijack 之后没有别人会替我们关。
func (c *WSConn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// readFrame 读一整帧，返回 opcode 与已解掩码的正文。
// 只接受单帧（FIN=1）、带掩码的客户端帧。
func (c *WSConn) readFrame() (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return 0, nil, err // 含 io.EOF：对端直接断线
	}
	if hdr[0]&0x80 == 0 {
		return 0, nil, errors.New("收到分片帧（FIN=0）：本实现只支持整帧消息，请让对端一次性发完")
	}
	op := hdr[0] & 0x0f
	if hdr[1]&0x80 == 0 {
		return 0, nil, errors.New("客户端帧没有掩码位：RFC6455 要求客户端发往服务端的帧必须掩码，请检查对端实现")
	}

	n := int64(hdr[1] & 0x7f)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		n = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		return 0, nil, fmt.Errorf("收到 64 位长度的帧：本实现不支持，单帧上限 %d 字节", wsMaxPayload)
	}
	if n > wsMaxPayload {
		return 0, nil, fmt.Errorf("帧正文 %d 字节，超过单帧上限 %d 字节，请让对端把消息拆小", n, wsMaxPayload)
	}

	var mask [4]byte
	if _, err := io.ReadFull(c.br, mask[:]); err != nil {
		return 0, nil, err
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return op, payload, nil
}

// writeFrame 写一帧：FIN=1，服务端不掩码。
func (c *WSConn) writeFrame(op byte, payload []byte) error {
	n := len(payload)
	if n > wsMaxPayload {
		return fmt.Errorf("要发的帧 %d 字节，超过单帧上限 %d 字节，请把消息拆小", n, wsMaxPayload)
	}

	var hdr []byte
	switch {
	case n < 126:
		hdr = []byte{0x80 | op, byte(n)}
	case n <= 0xffff:
		hdr = make([]byte, 4)
		hdr[0] = 0x80 | op
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
	default:
		return fmt.Errorf("要发的帧 %d 字节，本实现只支持 16 位长度（最大 %d 字节）", n, 0xffff)
	}

	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.conn.Write(hdr); err != nil {
		return err
	}
	if n > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}
