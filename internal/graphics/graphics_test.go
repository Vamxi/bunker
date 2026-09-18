package graphics

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

var testContext = Context{Cols: 80, Rows: 24, CellWidth: 8, CellHeight: 16}

func encodedPNG(t *testing.T) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	img.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
	img.SetNRGBA(0, 1, color.NRGBA{B: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestKittyTransferPlacementAndDeletion(t *testing.T) {
	var d Decoder
	encoded := encodedPNG(t)
	first, err := d.Handle('_', []byte("Ga=T,f=100,i=42,p=3,c=2,r=1,m=1;"+encoded[:20]), testContext)
	if err != nil || first.Paint != nil || first.Reply != "" {
		t.Fatalf("first chunk: %+v %v", first, err)
	}
	last, err := d.Handle('_', []byte("Gm=0;"+encoded[20:]), testContext)
	if err != nil || last.Paint == nil {
		t.Fatalf("final chunk: %+v %v", last, err)
	}
	if last.Reply != "\x1b_Gi=42,p=3;OK\x1b\\" || last.Paint.Cols != 2 || last.Paint.Rows != 1 {
		t.Fatalf("placement: %+v", last)
	}
	if got := last.Paint.Sample(image.Rect(0, 0, 8, 8)); got != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("red sample: %+v", got)
	}
	if got := last.Paint.Sample(image.Rect(8, 8, 16, 16)); got.A != 0 {
		t.Fatalf("transparent sample: %+v", got)
	}
	clone := d.Clone()
	deleted, err := d.Handle('_', []byte("Ga=d,d=I,i=42;"), testContext)
	if err != nil || deleted.Delete == nil || len(d.images) != 0 || d.bytes != 0 {
		t.Fatalf("delete: %+v %v", deleted, err)
	}
	placed, err := clone.Handle('_', []byte("Ga=p,i=42,C=1,x=1,y=0,w=1,h=1,c=1;"), testContext)
	if err != nil || placed.Paint.Move || placed.Paint.Source != image.Rect(1, 0, 2, 1) {
		t.Fatalf("cloned image: %+v %v", placed, err)
	}
	missing, err := d.Handle('_', []byte("Ga=p,i=42;"), testContext)
	if err == nil || !strings.Contains(missing.Reply, "ENOENT") {
		t.Fatalf("missing image: %+v %v", missing, err)
	}
}

func TestKittyRawCompressedAndQuery(t *testing.T) {
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	if _, err := w.Write([]byte{255, 0, 0, 0, 0, 255}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var d Decoder
	body := "Ga=q,i=7,f=24,s=2,v=1,o=z;" + base64.StdEncoding.EncodeToString(compressed.Bytes())
	result, err := d.Handle('_', []byte(body), testContext)
	if err != nil || result.Reply != "\x1b_Gi=7;OK\x1b\\" || len(d.images) != 0 || result.Paint != nil {
		t.Fatalf("query: %+v %v", result, err)
	}
	body = strings.Replace(body, "a=q", "a=T", 1)
	result, err = d.Handle('_', []byte(body), testContext)
	if err != nil || result.Paint.Image.NRGBAAt(1, 0) != (color.NRGBA{B: 255, A: 255}) {
		t.Fatalf("RGB upload: %+v %v", result, err)
	}
	result, err = d.Handle('_', []byte("Ga=T,f=32,s=1,v=1,q=2;"+base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 128})), testContext)
	if err != nil || result.Reply != "" || result.Paint.Image.NRGBAAt(0, 0).A != 128 {
		t.Fatalf("RGBA upload: %+v %v", result, err)
	}
}

func TestKittyErrorsAndLimits(t *testing.T) {
	for _, header := range []string{
		"a=T,t=f", "a=T,t=t", "a=T,t=s", "a=f", "a=p,U=1", "a=p,z=-1",
		"a=T,f=24,s=99999,v=1", "a=T,f=24,s=1,v=1", "a=T,f=42",
		"a=T,f=32,s=1,v=1,o=bad", "a=T,f=32,s=1,v=1,o=z", "a=T,m=2",
		"a=T,f=32,s=1,v=1,c=4294967295", "a=T,I=2", "a=p",
	} {
		t.Run(header, func(t *testing.T) {
			var d Decoder
			result, err := d.Handle('_', []byte("Gi=9,"+header+";AAAA/w=="), testContext)
			if err == nil || result.Paint != nil || !strings.Contains(result.Reply, ";E") {
				t.Fatalf("accepted invalid image: %+v %v", result, err)
			}
		})
	}
	var d Decoder
	if _, err := d.Handle('_', []byte("Gi=1,m=1;"+strings.Repeat("A", MaxSequenceBytes/2)), testContext); err != nil {
		t.Fatal(err)
	}
	r, err := d.Handle('_', []byte("Gm=0;"+strings.Repeat("A", MaxSequenceBytes/2+1)), testContext)
	if err == nil || d.kitty != nil || !strings.Contains(r.Reply, "E2BIG") {
		t.Fatal("multipart transfer bypassed limit")
	}
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	if _, err := w.Write(bytes.Repeat([]byte{0}, MaxPixels*4+1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = d.Handle('_', []byte("Gi=1,f=32,s=1,v=1,o=z;"+base64.StdEncoding.EncodeToString(compressed.Bytes())), testContext)
	if err == nil || !strings.Contains(err.Error(), "E2BIG") {
		t.Fatal("decompression limit not enforced")
	}
}

func TestKittyCacheAndQuiet(t *testing.T) {
	var d Decoder
	for i := 1; i <= maxImages+1; i++ {
		r, err := d.Handle('_', []byte(fmt.Sprintf("Gi=%d,f=24,s=1,v=1,q=1;/wAA", i)), testContext)
		if err != nil || r.Reply != "" {
			t.Fatalf("quiet upload: %+v %v", r, err)
		}
	}
	if len(d.images) != maxImages || d.bytes != maxImages*4 {
		t.Fatal("cache entry budget not enforced")
	}
	if _, exists := d.images[1]; exists {
		t.Fatal("oldest cached image not evicted")
	}
	r, err := d.Handle('_', []byte("Ga=p,i=1,q=1"), testContext)
	if err == nil || !strings.Contains(r.Reply, "ENOENT") {
		t.Fatal("q=1 suppressed error")
	}
	r, err = d.Handle('_', []byte("Ga=p,i=1,q=2"), testContext)
	if err == nil || r.Reply != "" {
		t.Fatal("q=2 did not suppress error")
	}
}

func TestITermFormatsSizesAndMultipart(t *testing.T) {
	encoded := encodedPNG(t)
	for _, tc := range []struct {
		args       string
		cols, rows int
	}{
		{"width=4", 4, 2}, {"height=2", 4, 2}, {"width=16px;height=16px", 2, 1},
		{"width=10%;height=25%;preserveAspectRatio=0", 8, 6},
		{"width=4;height=4", 4, 2},
	} {
		var d Decoder
		r, err := d.Handle(']', []byte("1337;File=inline=1;"+tc.args+":"+encoded), testContext)
		if err != nil || r.Paint == nil || r.Paint.Cols != tc.cols || r.Paint.Rows != tc.rows || !r.Paint.Inline {
			t.Fatalf("%s: %+v %v", tc.args, r.Paint, err)
		}
	}
	var d Decoder
	for _, part := range []string{"MultipartFile=inline=1;width=2", "FilePart=" + encoded[:20], "FilePart=" + encoded[20:]} {
		r, err := d.Handle(']', []byte("1337;"+part), testContext)
		if err != nil || r.Paint != nil {
			t.Fatalf("multipart: %+v %v", r, err)
		}
	}
	r, err := d.Handle(']', []byte("1337;FileEnd"), testContext)
	if err != nil || r.Paint == nil || d.iterm != nil {
		t.Fatalf("multipart end: %+v %v", r, err)
	}
	for _, format := range []string{"jpeg", "gif"} {
		var b bytes.Buffer
		img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
		var err error
		if format == "jpeg" {
			err = jpeg.Encode(&b, img, nil)
		} else {
			err = gif.Encode(&b, img, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		r, err := d.Handle(']', []byte("1337;File=inline=1:"+base64.StdEncoding.EncodeToString(b.Bytes())), testContext)
		if err != nil || r.Paint == nil {
			t.Fatalf("%s: %v", format, err)
		}
	}
	for _, body := range []string{"1337;File=inline=0:" + encoded, "1337;File=inline=1;width=999999999999999999999999px:" + encoded, "1337;File=inline=1:bad", "1337;FilePart=AAAA", "1337;FileEnd"} {
		if _, err := d.Handle(']', []byte(body), testContext); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestSixel(t *testing.T) {
	var d Decoder
	r, err := d.Handle('P', []byte("0;1q\"1;1;8;12#1;2;100;0;0!8~-#2;1;240;50;100!8~"), testContext)
	if err != nil || r.Paint == nil {
		t.Fatalf("SIXEL: %+v %v", r, err)
	}
	if got := r.Paint.Image.NRGBAAt(0, 0); got != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("red=%+v", got)
	}
	if got := r.Paint.Image.NRGBAAt(7, 11); got != (color.NRGBA{G: 255, A: 255}) {
		t.Fatalf("HLS green=%+v", got)
	}
	r, err = d.Handle('P', []byte("0;1q\"1;1;8;6#1@"), testContext)
	if err != nil || r.Paint.Image.NRGBAAt(0, 0).R != 255 || r.Paint.Image.NRGBAAt(7, 5).A != 0 {
		t.Fatalf("persistent palette/transparency: %+v %v", r, err)
	}
	for _, body := range []string{"q!999999~", "q#999~", "q\"1;1;4096;4096", "q#1;2;999;0;0~", "q!3", "q!0~", "q%", "q" + strings.Repeat("-", MaxDimension), "q" + strings.Repeat("!4096~$", MaxPixels*16/(4096*6)+1)} {
		if _, err := d.Handle('P', []byte(body), testContext); err == nil {
			t.Fatalf("accepted malformed or oversized SIXEL (%d bytes)", len(body))
		}
	}
}

func FuzzGraphicsDecoder(f *testing.F) {
	for _, seed := range []string{"Gf=24,s=1,v=1;/wAA", "1337;File=inline=1:AAAA", "0;1q#1;2;100;0;0!8~", "Gm=1;AAAA", "Gm=0;/w=="} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 65536 {
			return
		}
		var d Decoder
		for _, typ := range []rune{'_', ']', 'P'} {
			result, err := d.Handle(typ, []byte(body), testContext)
			if err != nil {
				continue
			}
			if result.Paint != nil {
				if result.Paint.Cols*result.Paint.Rows > maxDisplayCells {
					t.Fatal("unbounded placement")
				}
				_ = result.Paint.Sample(image.Rect(0, 0, 8, 8))
			}
		}
	})
}
