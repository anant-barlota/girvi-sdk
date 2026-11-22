package sandbox

import (
	"errors"
	"fmt"

	"github.com/anant-barlota/girvi-sdk/plugin/contract"
)

// precheck reads a module's sections before it is compiled and refuses sizes
// no real plugin declares. Compiling and instantiating allocate host memory in
// proportion to what a module declares (local variables, table entries, item
// counts), outside the plugin's memory limit, so a few hundred bytes could
// otherwise ask for gigabytes and take Girvi down before anything is approved.
//
// It checks only what it needs to: every vector's count against the bytes
// left in its section (each item takes at least one byte), local variables per
// function and in all, and table sizes. The compiler validates the rest.
func precheck(wasm []byte) error {
	r := reader{b: wasm}
	if len(wasm) < 8 || string(wasm[:4]) != "\x00asm" {
		return errors.New("not a WebAssembly module")
	}
	r.p = 8
	var totalLocals uint64
	for r.p < len(r.b) {
		id := r.byte()
		size := r.uint()
		if r.err != nil || uint64(len(r.b)-r.p) < size {
			return errors.New("a section runs past the end of the module")
		}
		s := reader{b: r.b[r.p : r.p+int(size)]}
		r.p += int(size)
		switch id {
		case 1, 2, 3, 5, 6, 7, 9, 11: // types, imports, functions, memories, globals, exports, elements, data
			s.count()
		case 4: // tables
			for n := s.count(); n > 0 && s.err == nil; n-- {
				s.byte() // element type
				minimum, maximum, hasMax := s.limits()
				if minimum > contract.MaxTableSize || (hasMax && maximum > contract.MaxTableSize) {
					return fmt.Errorf("a table of %d entries is larger than the %d allowed", max(minimum, maximum), contract.MaxTableSize)
				}
			}
		case 10: // code
			for n := s.count(); n > 0 && s.err == nil; n-- {
				bodySize := s.uint()
				if s.err != nil || uint64(len(s.b)-s.p) < bodySize {
					return errors.New("a function runs past the end of the module")
				}
				body := reader{b: s.b[s.p : s.p+int(bodySize)]}
				s.p += int(bodySize)
				var locals uint64
				for groups := body.count(); groups > 0 && body.err == nil; groups-- {
					locals += body.uint()
					body.byte() // value type
					if locals > contract.MaxFunctionLocals {
						return fmt.Errorf("a function declares more than %d local variables", contract.MaxFunctionLocals)
					}
				}
				if body.err != nil {
					return body.err
				}
				totalLocals += locals
				if totalLocals > contract.MaxModuleLocals {
					return fmt.Errorf("the module declares more than %d local variables in all", contract.MaxModuleLocals)
				}
			}
		}
		if s.err != nil {
			return s.err
		}
	}
	return r.err
}

// reader reads the LEB128 integers and bytes of a module section.
type reader struct {
	b   []byte
	p   int
	err error
}

func (r *reader) byte() byte {
	if r.err != nil || r.p >= len(r.b) {
		r.fail()
		return 0
	}
	r.p++
	return r.b[r.p-1]
}

// uint reads an unsigned LEB128 integer of at most 32 bits.
func (r *reader) uint() uint64 {
	var v uint64
	for shift := 0; shift < 35; shift += 7 {
		c := r.byte()
		if r.err != nil {
			return 0
		}
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v
		}
	}
	r.fail()
	return 0
}

// count reads a vector's length, which cannot exceed the bytes left: every
// item takes at least one.
func (r *reader) count() uint64 {
	n := r.uint()
	if r.err == nil && n > uint64(len(r.b)-r.p) {
		r.err = errors.New("a section claims more items than it holds")
		return 0
	}
	return n
}

func (r *reader) limits() (minimum, maximum uint64, hasMax bool) {
	flags := r.byte()
	minimum = r.uint()
	if flags&1 != 0 {
		maximum, hasMax = r.uint(), true
	}
	return minimum, maximum, hasMax
}

func (r *reader) fail() {
	if r.err == nil {
		r.err = errors.New("the module ends in the middle of a section")
	}
}
