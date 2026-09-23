package imageutil

import (
	"encoding/binary"
	"image"
	"image/color"
)

// exifMaxIFDEntries bounds the IFD scan; real files stay far below it.
const exifMaxIFDEntries = 512

// jpegOrientation returns the EXIF Orientation value (1-8) stored in the JPEG
// bytes, or 1 when the tag is absent or cannot be parsed. Only a bounded APP1
// segment is inspected; no other metadata is interpreted.
func jpegOrientation(data []byte) int {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 1 // not a marker: the segment chain is corrupt
		}
		marker := data[i+1]
		switch {
		case marker == 0xFF: // fill byte, keep scanning
			i++
			continue
		case marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7):
			i += 2 // standalone markers carry no length
			continue
		case marker == 0xDA: // start of scan: metadata is complete
			return 1
		}
		segLen := int(data[i+2])<<8 | int(data[i+3])
		if segLen < 2 || i+2+segLen > len(data) {
			return 1
		}
		if marker == 0xE1 {
			if orientation := exifOrientationFromAPP1(data[i+4 : i+2+segLen]); orientation != 0 {
				return orientation
			}
		}
		i += 2 + segLen
	}
	return 1
}

// exifOrientationFromAPP1 parses the TIFF header and IFD0 of an APP1 payload,
// looking only for tag 0x0112 (Orientation). It returns 0 when the payload is
// not EXIF or the tag is missing or malformed.
func exifOrientationFromAPP1(payload []byte) int {
	const exifHeader = "Exif\x00\x00"
	if len(payload) < len(exifHeader)+8 || string(payload[:len(exifHeader)]) != exifHeader {
		return 0
	}
	tiff := payload[len(exifHeader):]

	var order binary.ByteOrder
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		order = binary.LittleEndian
	case tiff[0] == 'M' && tiff[1] == 'M':
		order = binary.BigEndian
	default:
		return 0
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 0
	}
	ifdOffset := int(order.Uint32(tiff[4:8]))
	if ifdOffset < 8 || ifdOffset+2 > len(tiff) {
		return 0
	}
	entries := int(order.Uint16(tiff[ifdOffset : ifdOffset+2]))
	if entries > exifMaxIFDEntries {
		return 0
	}
	for i := range entries {
		entry := ifdOffset + 2 + i*12
		if entry+12 > len(tiff) {
			return 0
		}
		if order.Uint16(tiff[entry:entry+2]) != 0x0112 {
			continue
		}
		const shortType = 3
		if order.Uint16(tiff[entry+2:entry+4]) != shortType || order.Uint32(tiff[entry+4:entry+8]) != 1 {
			return 0
		}
		if value := int(order.Uint16(tiff[entry+8 : entry+10])); value >= 1 && value <= 8 {
			return value
		}
		return 0
	}
	return 0
}

// applyOrientation bakes an EXIF Orientation value into the pixels. Mirroring
// values (2, 4, 5, 7) are included; values 5-8 swap the axes.
func applyOrientation(img image.Image, orientation int) image.Image {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	dstWidth, dstHeight := width, height
	if orientation >= 5 {
		dstWidth, dstHeight = height, width
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dstWidth, dstHeight))
	for y := range height {
		for x := range width {
			var dx, dy int
			switch orientation {
			case 2:
				dx, dy = width-1-x, y
			case 3:
				dx, dy = width-1-x, height-1-y
			case 4:
				dx, dy = x, height-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = height-1-y, x
			case 7:
				dx, dy = height-1-y, width-1-x
			case 8:
				dx, dy = y, width-1-x
			default:
				dx, dy = x, y
			}
			dst.Set(dx, dy, color.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)))
		}
	}
	return dst
}
