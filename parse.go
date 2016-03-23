// Copyright (c) 2016, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package sh

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
)

func Parse(r io.Reader, name string) (Prog, error) {
	p := &parser{
		r:    bufio.NewReader(r),
		name: name,
		npos: position{
			line: 1,
			col:  1,
		},
	}
	p.push(&p.prog.Stmts)
	p.next()
	p.program()
	return p.prog, p.err
}

type parser struct {
	r    *bufio.Reader
	name string

	err error

	spaced bool

	ltok Token
	tok  Token
	lval string
	val  string

	// backup position to unread a rune
	bpos position

	lpos position
	pos  position
	npos position

	prog  Prog
	stack []interface{}
}

type position struct {
	line int
	col  int
}

var reserved = map[rune]bool{
	'\n': true,
	'&':  true,
	'>':  true,
	'<':  true,
	'|':  true,
	';':  true,
	'(':  true,
	')':  true,
	'$':  true,
}

// like reserved, but these are only reserved if at the start of a word
var starters = map[rune]bool{
	'{': true,
	'}': true,
	'#': true,
}

var space = map[rune]bool{
	' ':  true,
	'\t': true,
}

var quote = map[rune]bool{
	'"':  true,
	'\'': true,
	'`':  true,
}

var identRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func (p *parser) readRune() (rune, error) {
	r, _, err := p.r.ReadRune()
	if err != nil {
		if err == io.EOF {
			p.setEOF()
		} else {
			p.errPass(err)
		}
		return 0, err
	}
	p.moveWith(r)
	return r, nil
}

func (p *parser) moveWith(r rune) {
	p.bpos = p.npos
	if r == '\n' {
		p.npos.line++
		p.npos.col = 1
	} else {
		p.npos.col++
	}
}

func (p *parser) unreadRune() {
	if err := p.r.UnreadRune(); err != nil {
		panic(err)
	}
	p.npos = p.bpos
}

func (p *parser) readOnly(wanted rune) bool {
	// Don't use our read/unread wrappers to avoid unnecessary
	// position movement and unwanted calls to p.eof()
	r, _, err := p.r.ReadRune()
	if r == wanted {
		p.moveWith(r)
		return true
	}
	if err == nil {
		p.r.UnreadRune()
	}
	return false
}

func (p *parser) next() {
	p.lpos = p.pos
	var r rune
	p.spaced = false
	for {
		var err error
		if r, err = p.readRune(); err != nil {
			return
		}
		if !space[r] {
			break
		}
		p.spaced = true
	}
	p.pos = p.npos
	p.pos.col--
	switch {
	case r == '\\' && p.readOnly('\n'):
		p.next()
	case reserved[r] || starters[r]:
		switch r {
		case '#':
			p.advance(COMMENT, p.readLine())
		case '\n':
			p.advance('\n', "")
		default:
			p.advance(doToken(r, p.readOnly), "")
		}
	default:
		p.advance(LIT, p.readLit(r))
	}
}

func (p *parser) readLit(r rune) string {
	var rs []rune
	var q rune
runeLoop:
	for {
		appendRune := true
		switch {
		case q != '\'' && r == '\\': // escaped rune
			r, _ = p.readRune()
			if r != '\n' {
				rs = append(rs, '\\', r)
			}
			appendRune = false
		case q != '\'' && r == '$': // end of lit
			p.unreadRune()
			break runeLoop
		case q != 0: // rest of quoted cases
			if r == q {
				q = 0
			}
		case quote[r]: // start of a quoted string
			q = r
		case reserved[r] || space[r]: // end of word
			p.unreadRune()
			break runeLoop
		}
		if appendRune {
			rs = append(rs, r)
		}
		var err error
		if r, err = p.readRune(); err != nil {
			if q != 0 {
				p.errWanted(Token(q))
			}
			break
		}
	}
	return string(rs)
}

func (p *parser) advance(tok Token, val string) {
	if p.tok != EOF {
		p.ltok = p.tok
		p.lval = p.val
	}
	p.tok = tok
	p.val = val
}

func (p *parser) setEOF() {
	p.advance(EOF, "EOF")
}

func (p *parser) readUntil(tok Token) (string, bool) {
	var rs []rune
	for {
		r, err := p.readRune()
		if err != nil {
			return string(rs), false
		}
		if tok == doToken(r, p.readOnly) {
			return string(rs), true
		}
		rs = append(rs, r)
	}
}

func (p *parser) readUntilWant(tok Token) string {
	s, found := p.readUntil(tok)
	if !found {
		p.errWanted(tok)
	}
	return s
}

func (p *parser) readLine() string {
	s, _ := p.readUntil('\n')
	return s
}

// We can't simply have these as tokens as they can sometimes be valid
// words, e.g. `echo if`.
var reservedLits = map[Token]string{
	IF:    "if",
	THEN:  "then",
	ELIF:  "elif",
	ELSE:  "else",
	FI:    "fi",
	WHILE: "while",
	FOR:   "for",
	IN:    "in",
	DO:    "do",
	DONE:  "done",
}

func (p *parser) peek(tok Token) bool {
	return p.tok == tok || (p.tok == LIT && p.val == reservedLits[tok])
}

func (p *parser) got(tok Token) bool {
	if p.peek(tok) {
		p.next()
		return true
	}
	return false
}

func (p *parser) want(tok Token) {
	if !p.peek(tok) {
		p.errWanted(tok)
		return
	}
	p.next()
}

func (p *parser) errPass(err error) {
	if p.err == nil {
		p.err = err
	}
	p.setEOF()
}

type lineErr struct {
	fname string
	pos   position
	text  string
}

func (e lineErr) Error() string {
	return fmt.Sprintf("%s:%d:%d: %s", e.fname, e.pos.line, e.pos.col, e.text)
}

func (p *parser) posErr(pos position, format string, v ...interface{}) {
	p.errPass(lineErr{
		fname: p.name,
		pos:   pos,
		text:  fmt.Sprintf(format, v...),
	})
}

func (p *parser) curErr(format string, v ...interface{}) {
	p.posErr(p.pos, format, v...)
}

func (p *parser) errWantedStr(s string) {
	if p.tok == EOF {
		p.pos = p.npos
	}
	p.curErr("unexpected token %s - wanted %s", p.tok, s)
}

func (p *parser) errWanted(tok Token) {
	p.errWantedStr(tok.String())
}

func (p *parser) errAfterStr(s string) {
	p.curErr("unexpected token %s after %s", p.tok, s)
}

func (p *parser) add(n Node) {
	cur := p.stack[len(p.stack)-1]
	switch x := cur.(type) {
	case *[]Node:
		*x = append(*x, n)
	case *Node:
		if *x != nil {
			panic("single node set twice")
		}
		*x = n
	default:
		panic("unknown type in the stack")
	}
}

func (p *parser) pop() {
	p.stack = p.stack[:len(p.stack)-1]
}

func (p *parser) push(v interface{}) {
	p.stack = append(p.stack, v)
}

func (p *parser) popAdd(n Node) {
	p.pop()
	p.add(n)
}

func (p *parser) program() {
	p.commands()
}

func (p *parser) commands(stop ...Token) int {
	return p.commandsPropagating(false, stop...)
}

func (p *parser) commandsLimited(stop ...Token) int {
	return p.commandsPropagating(true, stop...)
}

func (p *parser) commandsPropagating(propagate bool, stop ...Token) (count int) {
	var cmdStop []Token
	if propagate {
		cmdStop = stop
	}
	for p.tok != EOF {
		for _, tok := range stop {
			if p.peek(tok) {
				return
			}
		}
		p.command(cmdStop...)
		count++
	}
	return
}

func litWord(val string) Word {
	return Word{Parts: []Node{Lit{Val: val}}}
}

func (p *parser) word() {
	var w Word
	p.push(&w.Parts)
parts:
	for p.tok != EOF {
		if len(w.Parts) > 0 && p.spaced {
			break parts
		}
		switch {
		case p.got(LIT):
			p.add(Lit{Val: p.lval})
		case p.got(EXP):
			switch {
			case p.peek(LBRACE):
				p.add(ParamExp{Text: p.readUntilWant(RBRACE)})
				p.next()
			case p.got(LPAREN):
				var cs CmdSubst
				p.push(&cs.Stmts)
				p.commandsLimited(RPAREN)
				p.popAdd(cs)
				p.want(RPAREN)
			default:
				p.want(LIT)
				p.add(Lit{Val: "$" + p.lval})
			}
		default:
			break parts
		}
	}
	if len(w.Parts) == 0 {
		p.errWantedStr("word")
	}
	p.popAdd(w)
}

func (p *parser) wordList() (count int) {
	var stop = [...]Token{SEMICOLON, '\n'}
	for p.tok != EOF {
		for _, tok := range stop {
			if p.got(tok) {
				return
			}
		}
		p.word()
		count++
	}
	return
}

func (p *parser) command(stop ...Token) {
	switch {
	case p.got(COMMENT):
		p.add(Comment{
			Text: p.lval,
		})
	case p.got('\n'), p.got(COMMENT):
		if p.tok != EOF {
			p.command()
		}
	case p.got(LPAREN):
		var sub Subshell
		p.push(&sub.Stmts)
		if p.commandsLimited(RPAREN) == 0 {
			p.errWantedStr("command")
		}
		p.want(RPAREN)
		p.popAdd(sub)
	case p.got(LBRACE):
		var bl Block
		p.push(&bl.Stmts)
		if p.commands(RBRACE) == 0 {
			p.errWantedStr("command")
		}
		p.want(RBRACE)
		p.popAdd(bl)
	case p.got(IF):
		var ifs IfStmt
		p.push(&ifs.Cond)
		p.command()
		p.pop()
		p.want(THEN)
		p.push(&ifs.ThenStmts)
		p.commands(FI, ELIF, ELSE)
		p.pop()
		p.push(&ifs.Elifs)
		for p.got(ELIF) {
			var elf Elif
			p.push(&elf.Cond)
			p.command()
			p.pop()
			p.want(THEN)
			p.push(&elf.ThenStmts)
			p.commands(FI, ELIF, ELSE)
			p.popAdd(elf)
		}
		if p.got(ELSE) {
			p.pop()
			p.push(&ifs.ElseStmts)
			p.commands(FI)
		}
		p.want(FI)
		p.popAdd(ifs)
	case p.got(WHILE):
		var whl WhileStmt
		p.push(&whl.Cond)
		p.command()
		p.pop()
		p.want(DO)
		p.push(&whl.DoStmts)
		p.commands(DONE)
		p.want(DONE)
		p.popAdd(whl)
	case p.got(FOR):
		var fr ForStmt
		p.want(LIT)
		fr.Name = Lit{Val: p.lval}
		p.want(IN)
		p.push(&fr.WordList)
		p.wordList()
		p.pop()
		p.want(DO)
		p.push(&fr.DoStmts)
		p.commands(DONE)
		p.want(DONE)
		p.popAdd(fr)
	case p.peek(LIT), p.peek(EXP):
		var cmd Command
		p.push(&cmd.Args)
		p.word()
		fpos := p.lpos
		fval := p.lval
		if p.got(LPAREN) {
			p.want(RPAREN)
			if !identRe.MatchString(fval) {
				p.posErr(fpos, "invalid func name %q", fval)
			}
			fun := FuncDecl{
				Name: Lit{Val: fval},
			}
			p.push(&fun.Body)
			p.command()
			p.pop()
			p.popAdd(fun)
			return
		}
	args:
		for p.tok != EOF {
			for _, tok := range stop {
				if p.peek(tok) {
					break args
				}
			}
			switch {
			case p.peek(LIT), p.peek(EXP):
				p.word()
			case p.got(LAND):
				p.binaryExpr(LAND, cmd)
				return
			case p.got(OR):
				p.binaryExpr(OR, cmd)
				return
			case p.got(LOR):
				p.binaryExpr(LOR, cmd)
				return
			case p.gotRedirect():
			case p.got(AND):
				cmd.Background = true
				fallthrough
			case p.got(SEMICOLON), p.got('\n'):
				break args
			default:
				p.errAfterStr("command")
			}
		}
		p.popAdd(cmd)
	default:
		p.errWantedStr("command")
	}
}

func (p *parser) binaryExpr(op Token, left Node) {
	b := BinaryExpr{Op: op}
	p.push(&b.Y)
	p.command()
	p.pop()
	b.X = left
	p.popAdd(b)
}

func (p *parser) gotRedirect() bool {
	var r Redirect
	p.push(&r.Obj)
	switch {
	case p.got(RDROUT):
		r.Op = RDROUT
		p.word()
	case p.got(APPEND):
		r.Op = APPEND
		p.word()
	case p.got(RDRIN):
		r.Op = RDRIN
		p.word()
	default:
		p.pop()
		return false
	}
	p.popAdd(r)
	return true
}
