package program

const (
	OP_APUSH = byte(iota + 1)
	OP_IPUSH
	OP_IADD
	OP_ISUB
	OP_PRINT
	OP_FAIL
	OP_SEND
	OP_MAKEALLOC
)
