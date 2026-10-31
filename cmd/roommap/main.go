//go:build js && wasm

// Command roommap is the browser half of a room's map: a canvas showing who
// has the map open and where each of them is pointing. TinyGo builds it to
// web/static/roommap.wasm (make wasm), and wasm_app.js starts it.
//
// It is the smallest program that exercises every rule of
// patterns/wasm-client.md honestly: a canvas redrawn from a socket, which htmx
// cannot do. It sends one thing — where the pointer is — and never says who is
// pointing: the server knows that from the session cookie (rule 6).
package main

import (
	"syscall/js"

	"github.com/andygeiss/baseline-reference/v5/internal/presence"
)

// sendEvery is the least time between two positions sent up. Twenty a second
// is smooth to the eye and under the server's cap of thirty.
const sendEvery = 50.0 // milliseconds, as event.timeStamp counts them

func main() {
	doc := js.Global().Get("document")
	canvas := doc.Call("querySelector", "[data-module]")
	if canvas.IsNull() {
		return
	}
	ctx := canvas.Call("getContext", "2d")
	style := js.Global().Call("getComputedStyle", doc.Get("documentElement"))
	colors := palette{
		bg:     token(style, "--color-surface"),
		dot:    token(style, "--color-accent"),
		text:   token(style, "--color-text"),
		border: token(style, "--color-border"),
	}

	loc := js.Global().Get("location")
	scheme := "ws://"
	if loc.Get("protocol").String() == "https:" {
		scheme = "wss://"
	}
	room := js.Global().Call("encodeURIComponent", canvas.Get("dataset").Get("room")).String()
	sock := js.Global().Get("WebSocket").New(scheme + loc.Get("host").String() + "/ws?room=" + room)
	sock.Set("binaryType", "arraybuffer")

	done := make(chan struct{})
	var seats []presence.Seat
	last := -sendEvery

	// gone ends the program once its canvas has left the page — a boosted
	// navigation swaps the body and leaves this module running otherwise.
	gone := func() bool {
		if canvas.Get("isConnected").Bool() {
			return false
		}
		sock.Call("close", 1000)
		return true
	}

	onMove := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if gone() {
			return nil
		}
		e := args[0]
		now := e.Get("timeStamp").Float()
		if now-last < sendEvery || sock.Get("readyState").Int() != 1 {
			return nil
		}
		last = now
		w, h := canvas.Get("clientWidth").Float(), canvas.Get("clientHeight").Float()
		if w == 0 || h == 0 {
			return nil
		}
		x := int(e.Get("offsetX").Float() / w * presence.Scale)
		y := int(e.Get("offsetY").Float() / h * presence.Scale)
		up := presence.EncodeUp(x, y)
		buf := js.Global().Get("Uint8Array").New(len(up))
		js.CopyBytesToJS(buf, up)
		sock.Call("send", buf)
		return nil
	})
	onMessage := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if gone() {
			return nil
		}
		data := js.Global().Get("Uint8Array").New(args[0].Get("data"))
		b := make([]byte, data.Get("length").Int())
		js.CopyBytesToGo(b, data)
		if s, ok := presence.DecodeDown(b); ok {
			seats = s
			draw(canvas, ctx, colors, seats, "")
		}
		return nil
	})
	onClose := js.FuncOf(func(_ js.Value, args []js.Value) any {
		draw(canvas, ctx, colors, nil, "The map closed. Reload to open it again.")
		close(done)
		return nil
	})

	canvas.Call("addEventListener", "pointermove", onMove)
	sock.Set("onmessage", onMessage)
	sock.Set("onclose", onClose)
	draw(canvas, ctx, colors, nil, "Connecting…")

	<-done
	canvas.Call("removeEventListener", "pointermove", onMove)
	onMove.Release()
	onMessage.Release()
	onClose.Release()
}

// palette is the page's own colours, read from app.css's tokens once, so the
// canvas follows the theme the rest of the page does (DESIGN.md).
type palette struct{ bg, dot, text, border string }

func token(style js.Value, name string) string {
	return style.Call("getPropertyValue", name).String()
}

// draw paints the whole map: the background, then a dot and a name for every
// seat, or a line of text when there is something to say instead.
func draw(canvas, ctx js.Value, c palette, seats []presence.Seat, note string) {
	// The backing store follows the element's size, times the screen's pixel
	// ratio, so a dot is sharp on a phone.
	ratio := js.Global().Get("devicePixelRatio").Float()
	w := canvas.Get("clientWidth").Float() * ratio
	h := canvas.Get("clientHeight").Float() * ratio
	if canvas.Get("width").Float() != w || canvas.Get("height").Float() != h {
		canvas.Set("width", w)
		canvas.Set("height", h)
	}
	ctx.Set("fillStyle", c.bg)
	ctx.Call("fillRect", 0, 0, w, h)
	ctx.Set("strokeStyle", c.border)
	ctx.Call("strokeRect", 0, 0, w, h)

	ctx.Set("font", js.ValueOf(fontPx(14*ratio)+"px system-ui, sans-serif"))
	ctx.Set("textBaseline", "middle")
	if note != "" {
		ctx.Set("fillStyle", c.text)
		ctx.Call("fillText", note, 12*ratio, 20*ratio)
		return
	}
	r := 6 * ratio
	for _, s := range seats {
		x := float64(s.X) / presence.Scale * w
		y := float64(s.Y) / presence.Scale * h
		ctx.Set("fillStyle", c.dot)
		ctx.Call("beginPath")
		ctx.Call("arc", x, y, r, 0, 6.2832)
		ctx.Call("fill")
		ctx.Set("fillStyle", c.text)
		ctx.Call("fillText", s.Name, x+r*1.6, y)
	}
}

// fontPx writes a whole number of pixels without strconv, which would be
// bytes in the module for one call.
func fontPx(px float64) string {
	n := int(px + 0.5)
	if n <= 0 {
		return "1"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
