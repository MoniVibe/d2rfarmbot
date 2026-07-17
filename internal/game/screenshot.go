package game

import (
	"image"
	"unsafe"

	"github.com/hectorgimenez/koolo/internal/utils/winproc"
)

func (gd *MemoryReader) Screenshot() image.Image {
	// PrintWindow blits the window's PHYSICAL pixels (1920x1080 fullscreen), while
	// GameAreaSize is the LOGICAL client rect a DPI-unaware process sees (1536x840 at 125%).
	// Capture at physical size or the right/bottom of the frame is silently cropped —
	// measured: the skill tree panel (right edge of screen) was missing from every shot.
	shotW := int(float64(gd.GameAreaSizeX) * physicalScale)
	shotH := int(float64(gd.GameAreaSizeY) * physicalScale)
	// Create a device context compatible with the window
	hdcWindow, _, _ := winproc.GetWindowDC.Call(uintptr(gd.HWND))
	hdcMem, _, _ := winproc.CreateCompatibleDC.Call(hdcWindow)
	hbmMem, _, _ := winproc.CreateCompatibleBitmap.Call(hdcWindow, uintptr(shotW), uintptr(shotH))
	_, _, _ = winproc.SelectObject.Call(hdcMem, hbmMem)

	// Use PrintWindow to copy the window into the bitmap
	winproc.PrintWindow.Call(uintptr(gd.HWND), hdcMem, 3) // use 3 to get window content only

	// map the bitmap structure
	bmpInfo := struct {
		BiSize            uint32
		BiWidth, BiHeight int32
		BiPlanes          uint16
		BiBitCount        uint16
		BiCompression     uint32
		BiSizeImage       uint32
		BiXPelsPerMeter   int32
		BiYPelsPerMeter   int32
		BiClrUsed         uint32
		BiClrImportant    uint32
	}{
		BiSize:        40, // The size of the BITMAPINFOHEADER structure
		BiWidth:       int32(shotW),
		BiHeight:      -int32(shotH), // negative to indicate top-down bitmap
		BiPlanes:      1,
		BiBitCount:    32, // 32 bits-per-pixel
		BiCompression: 0,  // BI_RGB, no compression
		BiSizeImage:   0,  // 0 for BI_RGB
	}

	bufSize := shotW * shotH * 4
	buf := make([]byte, bufSize)
	winproc.GetDIBits.Call(
		hdcMem,
		hbmMem,
		0,
		uintptr(shotH),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bmpInfo)),
		0, // DIB_RGB_COLORS
	)

	// Convert raw bytes to *image.RGBA
	img := image.NewRGBA(image.Rect(0, 0, shotW, shotH))
	copy(img.Pix, buf)

	// Windows is using BRG instead of RGB, let's swap red and blue layers
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			idx := y*img.Stride + x*4 // Calculate index for the start of the pixel
			// Swap red and blue (at idx and idx+2)
			img.Pix[idx], img.Pix[idx+2] = img.Pix[idx+2], img.Pix[idx]
		}
	}

	// Cleanup
	_, _, _ = winproc.DeleteObject.Call(hbmMem)
	_, _, _ = winproc.DeleteDC.Call(hdcMem)

	return img
}
