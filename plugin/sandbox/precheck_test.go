package sandbox

import (
	"context"
	"strings"
	"testing"

	"github.com/anant-barlota/girvi-sdk/plugin/manifest"
)

func leb(v uint64) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, c|0x80)
			continue
		}
		return append(out, c)
	}
}

func section(id byte, body ...[]byte) []byte {
	var b []byte
	for _, p := range body {
		b = append(b, p...)
	}
	return append(append([]byte{id}, leb(uint64(len(b)))...), b...)
}

// module builds a module exporting on_event, a function with the given local
// variable declarations and code, plus any extra sections.
func module(locals, code []byte, extra ...[]byte) []byte {
	m := []byte("\x00asm\x01\x00\x00\x00")
	m = append(m, section(1, leb(1), []byte{0x60, 0, 0})...) // one type: () -> ()
	m = append(m, section(3, leb(1), leb(0))...)             // one function of it
	for _, s := range extra {
		if s[0] == 4 {
			m = append(m, s...)
		}
	}
	m = append(m, section(7, leb(1), leb(8), []byte("on_event"), []byte{0}, leb(0))...)
	body := append(append(locals, code...), 0x0b)
	m = append(m, section(10, leb(1), leb(uint64(len(body))), body)...)
	return m
}

func compile(t *testing.T, wasm []byte) error {
	t.Helper()
	ctx := context.Background()
	e, err := NewEngine(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close(ctx)
	m, _ := manifest.Parse([]byte(`{"id":"a/b","name":"B","version":"1","api":"girvi/v1"}`))
	_, err = e.Compile(ctx, m, wasm)
	return err
}

// Modules whose declarations would make the compiler allocate gigabytes are
// refused before it runs, so installing one cannot take Girvi down.
func TestImpossibleModulesAreRefusedBeforeCompiling(t *testing.T) {
	if err := compile(t, module(leb(0), nil)); err != nil {
		t.Fatalf("a plain module: %v", err)
	}
	for name, c := range map[string]struct {
		wasm []byte
		want string
	}{
		// One function declaring 4.29 billion locals: 34 GB in the decoder.
		"locals":       {module(append(leb(1), append(leb(0xFFFFFFFF), 0x7f)...), nil), "local variables"},
		"many groups":  {module(append(leb(3), append(append(leb(30_000), 0x7f), append(leb(30_000), 0x7f)...)...), nil), "local variables"},
		"huge table":   {module(leb(0), nil, section(4, leb(1), []byte{0x70, 0}, leb(1<<27))), "table"},
		"huge maximum": {module(leb(0), nil, section(4, leb(1), []byte{0x70, 1}, leb(1), leb(1<<27))), "table"},
		"many tables":  {module(leb(0), nil, section(4, leb(6), []byte{0x70, 0, 1, 0x70, 0, 1, 0x70, 0, 1, 0x70, 0, 1, 0x70, 0, 1, 0x70, 0, 1})), "plugin.wasm is not valid"},
		"count":        {append([]byte("\x00asm\x01\x00\x00\x00"), section(3, leb(0xFFFFFFFF))...), "more items"},
		"truncated":    {append([]byte("\x00asm\x01\x00\x00\x00"), 3, 0x7f), "past the end"},
	} {
		err := compile(t, c.wasm)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error mentioning %q", name, err, c.want)
		}
	}
}

// A table cannot be grown while the plugin runs: table.grow needs reference
// types, which the runtime turns off.
func TestTablesCannotGrow(t *testing.T) {
	table := section(4, leb(1), []byte{0x70, 0}, leb(1))
	grow := []byte{0xd0, 0x70, 0x41}                                   // ref.null func, i32.const
	grow = append(append(grow, leb(1<<20)...), 0xfc, 0x0f, 0x00, 0x1a) // ... table.grow 0, drop
	if err := compile(t, module(leb(0), grow, table)); err == nil {
		t.Fatal("a module that grows its table was compiled")
	}
}
