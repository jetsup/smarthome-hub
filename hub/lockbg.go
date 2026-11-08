package hub

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// The lock-screen background of a display: the owner uploads any picture (from the website or
// the app); the hub turns it into exactly what the display needs - 320x240, 16-bit colour in
// the display's byte order, dithered so gradients do not band - and the display downloads it.

const (
	lockW, lockH   = 320, 240
	maxUploadBytes = 12 << 20
	maxPixels      = 60_000_000
)

// exifOrientation reads the orientation tag of a JPEG (1 when absent): phone photos are stored
// sideways with a flag saying how to turn them.
func exifOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 < len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		size := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if marker == 0xE1 && i+10 < len(b) && string(b[i+4:i+8]) == "Exif" {
			t := b[i+10:]
			end := i + 2 + size
			if end > len(b) {
				end = len(b)
			}
			t = b[i+10 : end]
			if len(t) < 14 {
				return 1
			}
			var bo binary.ByteOrder = binary.LittleEndian
			if t[0] == 'M' {
				bo = binary.BigEndian
			}
			ifd := int(bo.Uint32(t[4:8]))
			if ifd+2 > len(t) {
				return 1
			}
			n := int(bo.Uint16(t[ifd : ifd+2]))
			for k := 0; k < n; k++ {
				e := ifd + 2 + k*12
				if e+12 > len(t) {
					break
				}
				if bo.Uint16(t[e:e+2]) == 0x0112 {
					return int(bo.Uint16(t[e+8 : e+10]))
				}
			}
			return 1
		}
		if marker == 0xDA {
			break
		}
		i += 2 + size
	}
	return 1
}

// processLockBackground returns lockW*lockH*2 bytes (RGB565, high byte first).
// zoom >= 1 enlarges the picture; panX/panY (0..1) choose which part stays visible.
func processLockBackground(data []byte, zoom, panX, panY float64) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("That file is not a picture I can read (use JPEG, PNG or GIF)")
	}
	if cfg.Width < 16 || cfg.Height < 16 {
		return nil, fmt.Errorf("The picture is too small")
	}
	if cfg.Width*cfg.Height > maxPixels {
		return nil, fmt.Errorf("The picture is too large (over %d megapixels)", maxPixels/1_000_000)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("The picture could not be decoded")
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	orient := exifOrientation(data)
	ow, oh := w, h
	if orient == 6 || orient == 8 {
		ow, oh = h, w
	}
	// pixel (ox,oy) of the upright picture -> the stored pixel
	at := func(ox, oy int) (float64, float64, float64) {
		sx, sy := ox, oy
		switch orient {
		case 3:
			sx, sy = w-1-ox, h-1-oy
		case 6:
			sx, sy = oy, h-1-ox
		case 8:
			sx, sy = w-1-oy, ox
		}
		r, g, bl, _ := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
		return float64(r >> 8), float64(g >> 8), float64(bl >> 8)
	}

	if zoom < 1 {
		zoom = 1
	}
	if zoom > 4 {
		zoom = 4
	}
	panX = math.Min(1, math.Max(0, panX))
	panY = math.Min(1, math.Max(0, panY))
	// "cover": the largest 4:3 window of the picture, then zoomed in
	cw, ch := float64(ow), float64(ow)*lockH/lockW
	if ch > float64(oh) {
		ch, cw = float64(oh), float64(oh)*lockW/lockH
	}
	cw, ch = cw/zoom, ch/zoom
	x0 := (float64(ow) - cw) * panX
	y0 := (float64(oh) - ch) * panY
	stepX, stepY := cw/lockW, ch/lockH

	px := make([][3]float64, lockW*lockH)
	for dy := 0; dy < lockH; dy++ {
		for dx := 0; dx < lockW; dx++ {
			sx0, sx1 := x0+float64(dx)*stepX, x0+float64(dx+1)*stepX
			sy0, sy1 := y0+float64(dy)*stepY, y0+float64(dy+1)*stepY
			var r, g, bl, wsum float64
			if stepX <= 1 && stepY <= 1 { // enlarging: smooth between neighbours
				cx, cy := (sx0+sx1)/2-0.5, (sy0+sy1)/2-0.5
				ix, iy := int(math.Floor(cx)), int(math.Floor(cy))
				fx, fy := cx-float64(ix), cy-float64(iy)
				for j := 0; j < 2; j++ {
					for i := 0; i < 2; i++ {
						xx, yy := clampInt(ix+i, 0, ow-1), clampInt(iy+j, 0, oh-1)
						wt := (1 - math.Abs(float64(i)-fx)) * (1 - math.Abs(float64(j)-fy))
						rr, gg, bb := at(xx, yy)
						r, g, bl, wsum = r+rr*wt, g+gg*wt, bl+bb*wt, wsum+wt
					}
				}
			} else { // shrinking: average everything the output pixel covers
				for yy := int(math.Floor(sy0)); yy < int(math.Ceil(sy1)) && yy < oh; yy++ {
					wy := math.Min(sy1, float64(yy+1)) - math.Max(sy0, float64(yy))
					for xx := int(math.Floor(sx0)); xx < int(math.Ceil(sx1)) && xx < ow; xx++ {
						wx := math.Min(sx1, float64(xx+1)) - math.Max(sx0, float64(xx))
						wt := wx * wy
						if wt <= 0 {
							continue
						}
						rr, gg, bb := at(xx, yy)
						r, g, bl, wsum = r+rr*wt, g+gg*wt, bl+bb*wt, wsum+wt
					}
				}
			}
			if wsum > 0 {
				px[dy*lockW+dx] = [3]float64{r / wsum, g / wsum, bl / wsum}
			}
		}
	}

	// 5-6-5 has only 32/64 levels per channel; Floyd-Steinberg error diffusion hides the steps.
	out := make([]byte, lockW*lockH*2)
	levels := [3]float64{31, 63, 31}
	for y := 0; y < lockH; y++ {
		for x := 0; x < lockW; x++ {
			p := &px[y*lockW+x]
			var q [3]int
			for c := 0; c < 3; c++ {
				v := math.Min(255, math.Max(0, p[c]))
				l := math.Round(v / 255 * levels[c])
				q[c] = int(l)
				errv := v - l/levels[c]*255
				diffuse := func(dx, dy int, f float64) {
					nx, ny := x+dx, y+dy
					if nx >= 0 && nx < lockW && ny < lockH {
						px[ny*lockW+nx][c] += errv * f
					}
				}
				diffuse(1, 0, 7.0/16)
				diffuse(-1, 1, 3.0/16)
				diffuse(0, 1, 5.0/16)
				diffuse(1, 1, 1.0/16)
			}
			v := uint16(q[0]<<11 | q[1]<<5 | q[2])
			o := (y*lockW + x) * 2
			out[o], out[o+1] = byte(v>>8), byte(v)
		}
	}
	return out, nil
}

// lockBgPreview turns the stored pixels back into a PNG for the website and the app.
func lockBgPreview(raw []byte) ([]byte, error) {
	if len(raw) != lockW*lockH*2 {
		return nil, fmt.Errorf("no picture")
	}
	img := image.NewRGBA(image.Rect(0, 0, lockW, lockH))
	for i := 0; i < lockW*lockH; i++ {
		v := uint16(raw[i*2])<<8 | uint16(raw[i*2+1])
		r, g, b := byte(v>>11&31), byte(v>>5&63), byte(v&31)
		img.Set(i%lockW, i/lockW, color.RGBA{r<<3 | r>>2, g<<2 | g>>4, b<<3 | b>>2, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// lvglBinHeader is the 12-byte header of an LVGL image file (RGB565, byte-swapped, 320x240).
func lvglBinHeader() []byte {
	h := make([]byte, 12)
	h[0], h[1] = 0x19, 0x1B // magic, RGB565_SWAPPED
	binary.LittleEndian.PutUint16(h[4:], lockW)
	binary.LittleEndian.PutUint16(h[6:], lockH)
	binary.LittleEndian.PutUint16(h[8:], lockW*2)
	return h
}

// POST /api/hmis/:id/background  (multipart: image, optional zoom, panX, panY)
func UploadHmiBackground(c *gin.Context) {
	h, ok := ownedHmi(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadBytes+(1<<20))
	fh, err := c.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Choose a picture to upload"})
		return
	}
	if fh.Size > maxUploadBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "The picture is larger than 12 MB"})
		return
	}
	f, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Could not read the upload"})
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxUploadBytes+1))
	if err != nil || len(data) > maxUploadBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "The picture is larger than 12 MB"})
		return
	}
	num := func(k string, def float64) float64 {
		if v, e := strconv.ParseFloat(c.PostForm(k), 64); e == nil {
			return v
		}
		return def
	}
	raw, err := processLockBackground(data, num("zoom", 1), num("panX", 0.5), num("panY", 0.5))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ver := int(time.Now().Unix() % 2147483000)
	DB.Model(&Hmi{}).Where("id = ?", h.ID).Updates(map[string]interface{}{"lock_bg": raw, "lock_bg_version": ver})
	LogAudit(c, "hmi_lock_background", "gateway", &h.GatewayID, "Lock-screen picture of \""+h.Name+"\" changed")
	c.JSON(http.StatusOK, gin.H{"version": ver})
}

// GET /api/hmis/:id/background -> PNG preview
func GetHmiBackground(c *gin.Context) {
	h, ok := ownedHmi(c)
	if !ok {
		return
	}
	var row Hmi
	DB.Select("id", "lock_bg", "lock_bg_version").First(&row, h.ID)
	if len(row.LockBg) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "No picture"})
		return
	}
	png, err := lockBgPreview(row.LockBg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not read the picture"})
		return
	}
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "image/png", png)
}

// DELETE /api/hmis/:id/background
func DeleteHmiBackground(c *gin.Context) {
	h, ok := ownedHmi(c)
	if !ok {
		return
	}
	DB.Model(&Hmi{}).Where("id = ?", h.ID).Updates(map[string]interface{}{"lock_bg": nil, "lock_bg_version": 0})
	c.JSON(http.StatusOK, gin.H{"status": "removed"})
}

// GET /api/hmi/lockbg (display token) -> the picture as an LVGL image file
func HMILockBackground(c *gin.Context) {
	var row Hmi
	if err := DB.Select("id", "lock_bg", "lock_bg_version").First(&row, c.GetUint("hmiID")).Error; err != nil || len(row.LockBg) != lockW*lockH*2 {
		c.JSON(http.StatusNotFound, gin.H{"error": "No picture"})
		return
	}
	c.Header("X-Version", strconv.Itoa(row.LockBgVersion))
	c.Data(http.StatusOK, "application/octet-stream", append(lvglBinHeader(), row.LockBg...))
}
