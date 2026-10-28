// Command genicon 生成 exe 用的多尺寸图标 assets/app.ico。
// 图案与 assets/mark.svg 一致：灰色底环 + 42% 绿色进度弧 + 中心绿点。
// 用法（在仓库根目录）：go run ./tools/genicon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

var (
	green = color.NRGBA{0x34, 0xC7, 0x59, 0xFF}
	track = color.NRGBA{0x8E, 0x8E, 0x93, 0x73} // stroke-opacity 0.45
)

// draw 按 128 单位的 mark.svg 坐标渲染 size×size 图像，4×4 超采样抗锯齿。
func draw(size int) *image.NRGBA {
	const samples = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	k := 128.0 / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					px := (float64(x)+(float64(sx)+0.5)/samples)*k - 64
					py := (float64(y)+(float64(sy)+0.5)/samples)*k - 64
					d := math.Hypot(px, py)
					var c color.NRGBA
					switch {
					case d >= 38 && d <= 54:
						angle := math.Mod(math.Atan2(px, -py)+2*math.Pi, 2*math.Pi) / (2 * math.Pi)
						if angle <= 0.42 {
							c = green
						} else {
							c = track
						}
					case d <= 15:
						c = green
					default:
						continue
					}
					w := float64(c.A) / 255
					r, g, b, a = r+float64(c.R)*w, g+float64(c.G)*w, b+float64(c.B)*w, a+w
				}
			}
			if a > 0 {
				img.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / samples / samples * 255)})
			}
		}
	}
	return img
}

func main() {
	sizes := []int{16, 20, 24, 32, 40, 48, 64, 128, 256}
	var images [][]byte
	for _, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, draw(s)); err != nil {
			log.Fatal(err)
		}
		images = append(images, buf.Bytes())
	}
	// ICO：ICONDIR + 每个尺寸一条 ICONDIRENTRY，图像数据用 PNG（Vista 起支持）。
	var out bytes.Buffer
	le := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	le([3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := uint8(s % 256) // 256 写作 0
		le([4]uint8{dim, dim, 0, 0})
		le([2]uint16{1, 32})
		le([2]uint32{uint32(len(images[i])), uint32(offset)})
		offset += len(images[i])
	}
	for _, img := range images {
		out.Write(img)
	}
	if err := os.WriteFile("assets/app.ico", out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("assets/app.ico: %d sizes, %d bytes", len(sizes), out.Len())
}
