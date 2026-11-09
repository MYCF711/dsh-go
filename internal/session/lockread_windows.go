//go:build windows

package session

import "golang.org/x/sys/windows"

// readLockFileShared 读锁文件内容，但句柄带 FILE_SHARE_DELETE。
//
// Go 的 os.Open 在 Windows 上只带 SHARE_READ|SHARE_WRITE；只要读句柄还开着，
// 持有者的 os.Remove 就会报 sharing violation，把锁留成永久锁。锁内容读取
// 必须用带 SHARE_DELETE 的 CreateFile，才和“一边读、一边删”的协议兼容。
func readLockFileShared(path string) ([]byte, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	buf := make([]byte, 512)
	var n uint32
	if err := windows.ReadFile(h, buf, &n, nil); err != nil {
		return nil, err
	}
	return buf[:n], nil
}
