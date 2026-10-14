package jit

// Emitter is the receiver goasm generates every Emit* method on. The
// fixture's encoders record nothing: the graph needs the declarations,
// not the machine code.
type Emitter struct {
	Code []byte
}

func (e *Emitter) Movz(rd uint32, imm uint16, shift uint32) {}
func (e *Emitter) LdrReg(rt, rn, rm uint32)                 {}
func (e *Emitter) StrReg(rt, rn, rm uint32)                 {}
func (e *Emitter) AddReg(rd, rn, rm uint32)                 {}
func (e *Emitter) SubReg(rd, rn, rm uint32)                 {}
func (e *Emitter) StrImm(rt int, rn uint32, imm uint32)     {}
func (e *Emitter) AddImm(rd, rn uint32, imm uint64)         {}
