package compiler

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/antlr/antlr4/runtime/Go/antlr"
	"github.com/numary/machine/core"
	"github.com/numary/machine/script/parser"
	"github.com/numary/machine/vm/program"
)

type parseVisitor struct {
	elistener    *ErrorListener
	instructions []byte
	constants    []core.Value /* must not exceed 32768 elements */
	variables    map[string]program.VarInfo
}

// Allocates constants if it hasn't already been,
// and returns its resource address.
func (p *parseVisitor) AllocateConstant(v core.Value) (core.Address, error) {
	for i := 0; i < len(p.constants); i++ {
		if p.constants[i] == v {
			return core.Address(i), nil
		}
	}
	if len(p.constants) >= 32768 {
		return 0, errors.New("number of unique constants exceeded 32768")
	}
	p.constants = append(p.constants, v)
	return core.Address(len(p.constants) - 1), nil
}

func (p *parseVisitor) PushValue(val core.Value) error {
	switch val := val.(type) {
	case core.Account, core.Asset, core.Monetary:
		p.instructions = append(p.instructions, program.OP_APUSH)
		addr, err := p.AllocateConstant(val)
		if err != nil {
			return err
		}
		bytes := addr.ToBytes()
		p.instructions = append(p.instructions, bytes...)
	case core.Number:
		p.instructions = append(p.instructions, program.OP_IPUSH)
		bytes := make([]byte, 8)
		binary.LittleEndian.PutUint64(bytes, uint64(val))
		p.instructions = append(p.instructions, bytes...)
	default:
		panic("internal compiler error")
	}
	return nil
}

func (p *parseVisitor) VisitScript(c parser.IScriptContext) error {
	switch c := c.(type) {
	case *parser.ScriptContext:
		vars := c.GetVars()
		if vars != nil {
			switch c := vars.(type) {
			case *parser.VarListDeclContext:
				p.VisitVars(c)
			default:
				panic("internal compiler error")
			}
		}
		for _, stmt := range c.GetStmts() {
			switch c := stmt.(type) {
			case *parser.PrintContext:
				err := p.VisitPrint(c)
				if err != nil {
					p.elistener.LogicError(c.GetParser().GetCurrentToken(), err)
					return err
				}
			case *parser.FailContext:
				p.instructions = append(p.instructions, program.OP_FAIL)
			case *parser.SendContext:
				err := p.VisitSend(c)
				if err != nil {
					p.elistener.LogicError(c.GetParser().GetCurrentToken(), err)
					return err
				}
			default:
				panic("Invalid context")
			}
		}
	default:
		panic("Invalid context")
	}
	return nil
}

func (p *parseVisitor) VisitVars(c *parser.VarListDeclContext) error {
	if len(c.GetV()) > 32768 {
		return fmt.Errorf("number of variables exceeded %v", 32768)
	}
	for _, v := range c.GetV() {
		name := v.GetName().GetText()[1:]
		if _, ok := p.variables[name]; ok {
			return fmt.Errorf("duplicate variable: %v", name)
		}
		var ty core.Type
		switch v.GetTy().GetText() {
		case "account":
			ty = core.TYPE_ACCOUNT
		case "asset":
			ty = core.TYPE_ASSET
		case "number":
			ty = core.TYPE_NUMBER
		case "monetary":
			ty = core.TYPE_MONETARY
		}
		addr := core.NewVarAddress(uint16(len(p.variables)))
		p.variables[name] = program.VarInfo{
			Ty:   ty,
			Addr: addr,
		}
	}
	return nil
}

func (p *parseVisitor) VisitPrint(ctx *parser.PrintContext) error {
	_, err := p.VisitExpr(ctx.GetExpr())
	if err != nil {
		return err
	}
	p.instructions = append(p.instructions, program.OP_PRINT)
	return nil
}

func (p *parseVisitor) VisitExpr(ctx parser.IExpressionContext) (core.Type, error) {
	switch ctx := ctx.(type) {
	case *parser.ExprAddSubContext:
		ty, err := p.VisitExpr(ctx.GetLhs())
		if err != nil {
			return 0, err
		}
		if ty != core.TYPE_NUMBER {
			return 0, errors.New("tried to do arithmetic with wrong type")
		}
		ty, err = p.VisitExpr(ctx.GetRhs())
		if err != nil {
			return 0, nil
		}
		if ty != core.TYPE_NUMBER {
			return 0, errors.New("tried to do arithmetic with wrong type")
		}
		switch ctx.GetOp().GetTokenType() {
		case parser.NumScriptLexerOP_ADD:
			p.instructions = append(p.instructions, program.OP_IADD)
		case parser.NumScriptLexerOP_SUB:
			p.instructions = append(p.instructions, program.OP_ISUB)
		}
		return core.TYPE_NUMBER, nil
	case *parser.ExprLiteralContext:
		ty, err := p.VisitLit(ctx.GetLit())
		if err != nil {
			return 0, err
		}
		return ty, nil
	case *parser.ExprVariableContext:
		name := ctx.GetVariable().GetText()[1:]
		if info, ok := p.variables[name]; ok {
			p.instructions = append(p.instructions, program.OP_APUSH)
			bytes := info.Addr.ToBytes()
			p.instructions = append(p.instructions, bytes...)
			return info.Ty, nil
		} else {
			return 0, fmt.Errorf("variable not declared : %v", name)
		}
	default:
		panic("internal compiler error")
	}
}

// pushes a value from a literal onto the stack
func (p *parseVisitor) VisitLit(c parser.ILiteralContext) (core.Type, error) {
	switch c := c.(type) {
	case *parser.LitAccountContext:
		addr := core.Account(c.GetText()[1:])
		err := p.PushValue(addr)
		if err != nil {
			return 0, err
		}
		return core.TYPE_ACCOUNT, nil
	case *parser.LitAssetContext:
		asset := core.Asset(c.GetText())
		err := p.PushValue(asset)
		if err != nil {
			return 0, err
		}
		return core.TYPE_ASSET, nil
	case *parser.LitNumberContext:
		n, err := strconv.ParseUint(c.GetText(), 10, 64)
		if err != nil {
			return 0, err
		}
		number := core.Number(n)
		p.PushValue(number) // does not fail with numbers
		return core.TYPE_NUMBER, nil
	case *parser.LitMonetaryContext:
		asset := c.Monetary().GetAsset().GetText()
		amount, err := strconv.ParseUint(c.Monetary().GetAmount().GetText(), 10, 64)
		if err != nil {
			return 0, err
		}
		monetary := core.Monetary{
			Asset:  asset,
			Amount: amount,
		}
		err = p.PushValue(monetary)
		if err != nil {
			return 0, err
		}
		return core.TYPE_MONETARY, nil
	case *parser.LitAllocationContext:
		total := big.NewRat(0, 1)
		for _, v := range c.Allocation().GetParts() {
			rfrac := v.GetFr()
			frac, err := p.VisitFrac(rfrac)
			if err != nil {
				return 0, err
			}
			total.Add(frac, total)
			p.PushValue(core.Number(frac.Num().Uint64()))
			p.PushValue(core.Number(frac.Denom().Uint64()))
			ty, err := p.VisitExpr(v.GetDest())
			if ty != core.TYPE_ACCOUNT {
				return 0, errors.New("expected account as destination of allocation line")
			}
		}
		if total.Cmp(big.NewRat(1, 1)) != 0 {
			return 0, errors.New("sum of fractions did not equal 100%")
		}
		length := len(c.Allocation().GetParts())
		p.PushValue(core.Number(length))
		p.instructions = append(p.instructions, program.OP_MAKEALLOC)
		return core.TYPE_ALLOCATION, nil
	default:
		panic("internal compiler error")
	}
}

func (p *parseVisitor) VisitFrac(c parser.IFracContext) (*big.Rat, error) {
	switch c := c.(type) {
	case *parser.RatioContext:
		v := strings.Split(c.GetR().GetText(), "/")
		ns := strings.Trim(v[0], " \t")
		ds := strings.Trim(v[1], " \t")
		n, err := strconv.ParseInt(ns, 10, 64)
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			return nil, errors.New("numerator must be greater than zero")
		}
		d, err := strconv.ParseInt(ds, 10, 64)
		if err != nil {
			return nil, err
		}
		if d <= 0 {
			return nil, errors.New("denominator must be greater than zero")
		}
		return big.NewRat(int64(n), int64(d)), nil
	case *parser.PercentageContext:
		n, err := strconv.ParseInt(c.GetP().GetText(), 10, 64)
		if err != nil {
			return nil, err
		}
		if n <= 0 || n >= 100 {
			return nil, errors.New("percentage must be greater than zero and less than 100")
		}
		return big.NewRat(n, 100), nil
	default:
		panic("internal compiler error")
	}
}

func (p *parseVisitor) VisitSend(c *parser.SendContext) error {
	ty, err := p.VisitExpr(c.GetMon())
	if err != nil {
		return err
	}
	if ty != core.TYPE_MONETARY {
		return errors.New("wrong type for monetary value")
	}

	ty, err = p.VisitExpr(c.GetSrc())
	if err != nil {
		return err
	}
	if ty != core.TYPE_ACCOUNT {
		return errors.New("wrong type for source")
	}
	ty, err = p.VisitExpr(c.GetDest())
	if err != nil {
		return err
	}
	if ty != core.TYPE_ALLOCATION && ty != core.TYPE_ACCOUNT {
		return errors.New("wrong type for destination")
	}
	p.instructions = append(p.instructions, program.OP_SEND)
	return nil
}

type arg struct {
	name string
	ty   core.Type
}

func Compile(input string) (*program.Program, error) {
	elistener := &ErrorListener{}

	is := antlr.NewInputStream(input)
	lexer := parser.NewNumScriptLexer(is)
	lexer.RemoveErrorListeners()
	lexer.AddErrorListener(elistener)

	stream := antlr.NewCommonTokenStream(lexer, antlr.LexerDefaultTokenChannel)
	p := parser.NewNumScriptParser(stream)
	p.RemoveErrorListeners()
	p.AddErrorListener(elistener)

	p.BuildParseTrees = true

	tree := p.Script()

	if len(elistener.Errors) != 0 {
		return nil, (*CompileErrorList)(&elistener.Errors)
	}

	visitor := parseVisitor{
		elistener:    elistener,
		instructions: make([]byte, 0),
		constants:    make([]core.Value, 0),
		variables:    make(map[string]program.VarInfo, 0),
	}

	_ = visitor.VisitScript(tree)

	if len(elistener.Errors) != 0 {
		return nil, (*CompileErrorList)(&elistener.Errors)
	}

	return &program.Program{
		Instructions: visitor.instructions,
		Constants:    visitor.constants,
		Variables:    visitor.variables,
	}, nil
}
