//go:build windows

package cua

// 应用图标提取（官方 native `applicationIcons`，`dsh-cua/src/access.ts:95-101`）。
//
// 官方把图标从决策里拆出来单独一个方法，理由是"权限页不该被图标拖慢"；
// 本仓在同一位置上做真事：从可执行文件的图标资源里取一张真图，
// 编码成官方 `ApplicationIcon` 形状（`dsh-cua/src/protocol.ts:178`）：
//
//	data:image/png;base64,<canonical base64>，长度 ≤ 65536
//
// 三步：`SHGetFileInfoW(SHGFI_ICON|SHGFI_LARGEICON)` 拿 HICON →
// 画进 32bpp 自上而下的 DIBSection（`DrawIconEx`）→ 从位里读像素编码 PNG。
//
// 为什么固定在 64×64 画布而不是用图标原尺寸：系统里的图标尺寸从 16 到 256 不等，
// 256×256 的 PNG base64 可能顶到官方 65536 上限；64×64 是权限页真正显示的大小，
// 编码后通常 3–8 KB，离上限很远。**不是把原图裁掉**——是按目标尺寸重绘。

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"unsafe"
)

// win32Icons 是 Windows 上的真实图标提取。
type win32Icons struct{}

// NewIconProvider 返回 Windows 上的真实图标提取实现。
func NewIconProvider() IconProvider { return win32Icons{} }

// Icons 逐条取图标。**取不到就不出现在结果里**（契约允许"没有图标"）；
// 单条失败不拖垮整批——一个需要管理员权限的目录不该让整个权限页没图标。
func (win32Icons) Icons(paths []string) (map[string]string, error) {
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		dataURL, ok := iconDataURL(path)
		if !ok {
			continue
		}
		if len(dataURL) > MaxApplicationIconChars {
			continue
		}
		out[path] = dataURL
	}
	return out, nil
}

// iconDataURL 取一个可执行文件的图标并编码成 data URL。
func iconDataURL(executablePath string) (string, bool) {
	icon := shellIcon(executablePath)
	if icon == 0 {
		return "", false
	}
	defer procDestroyIcon.Call(icon)
	pixels, ok := drawIconRGBA(icon, iconCanvas)
	if !ok {
		return "", false
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		return "", false
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes()), true
}

// shellIcon 用 shell 的图标提取拿 HICON（调用方负责 DestroyIcon）。
func shellIcon(executablePath string) uintptr {
	path, err := utf16Ptr(executablePath)
	if err != nil || path == nil {
		return 0
	}
	var info shFileInfo
	ok, _, _ := procSHGetFileInfoW.Call(
		uintptr(unsafe.Pointer(path)), 0,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info),
		uintptr(shgfiIcon|shgfiLargeIcon),
	)
	if ok == 0 || info.hIcon == 0 {
		return 0
	}
	return info.hIcon
}

// drawIconRGBA 把 HICON 画进 32bpp 自上而下的 DIB 并读成 RGBA。
func drawIconRGBA(icon uintptr, size int) (*image.RGBA, bool) {
	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return nil, false
	}
	defer procReleaseDC.Call(0, screen)
	memory, _, _ := procCreateCompatibleDC.Call(screen)
	if memory == 0 {
		return nil, false
	}
	defer procDeleteDC.Call(memory)

	var info bitmapInfo
	info.bmiHeader.biSize = uint32(unsafe.Sizeof(info.bmiHeader))
	info.bmiHeader.biWidth = int32(size)
	info.bmiHeader.biHeight = -int32(size) // 负高 = 自上而下，省一次翻转
	info.bmiHeader.biPlanes = 1
	info.bmiHeader.biBitCount = 32
	info.bmiHeader.biCompression = biRGB

	var bits unsafe.Pointer
	section, _, _ := procCreateDIBSection.Call(memory, uintptr(unsafe.Pointer(&info)),
		uintptr(dibRGBColors), uintptr(unsafe.Pointer(&bits)), 0, 0)
	if section == 0 || bits == nil {
		return nil, false
	}
	defer procDeleteObject.Call(section)

	previous, _, _ := procSelectObject.Call(memory, section)
	// CreateDIBSection 给的位**不是清零的**：不清就会出现上一次的垃圾像素，
	// 而且没有 alpha 的图标会把垃圾当底色画出来。
	raw := unsafe.Slice((*byte)(bits), size*size*4)
	clear(raw)

	drawn, _, _ := procDrawIconEx.Call(memory, 0, 0, icon, uintptr(size), uintptr(size), 0, 0, drawnormal)
	if previous != 0 {
		procSelectObject.Call(memory, previous)
	}
	if drawn == 0 {
		return nil, false
	}

	pixels := image.NewRGBA(image.Rect(0, 0, size, size))
	opaque := false
	for index := 0; index < size*size; index++ {
		blue := raw[index*4]
		green := raw[index*4+1]
		red := raw[index*4+2]
		alpha := raw[index*4+3]
		if alpha == 0 {
			continue
		}
		opaque = true
		// GDI 里 32bpp 位图的 alpha 是**预乘**的（Bitmap AlphaBlend 语义），
		// 而 image.RGBA 要非预乘；不还原会让半透明边缘发暗。
		if alpha != 0xFF {
			red = unPremultiply(red, alpha)
			green = unPremultiply(green, alpha)
			blue = unPremultiply(blue, alpha)
		}
		pixels.Pix[index*4] = red
		pixels.Pix[index*4+1] = green
		pixels.Pix[index*4+2] = blue
		pixels.Pix[index*4+3] = alpha
	}
	if !opaque {
		// 全透明的图不是图标（多半是绘制失败），宁可没图标也不给一张空图。
		return nil, false
	}
	return pixels, true
}

func unPremultiply(value, alpha byte) byte {
	scale := uint32(value) * 0xFF
	result := (scale + uint32(alpha)/2) / uint32(alpha)
	if result > 0xFF {
		return 0xFF
	}
	return byte(result)
}
