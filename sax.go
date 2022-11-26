package sax

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	langle     = '<'
	rangle     = '>'
	slash      = '/'
	equal      = '='
	dquote     = '"'
	squote     = '\''
	mark       = '?'
	bang       = '!'
	tab        = '\t'
	space      = ' '
	nl         = '\n'
	cr         = '\r'
	underscore = '_'
	hyphen     = '-'
	lsquare    = '['
	rsquare    = ']'
	colon      = ':'
	ampersand  = '&'
	semicolon  = ';'
	pound      = '#'
)

var (
	ErrSkip        = errors.New("skip")
	ErrIgnore      = errors.New("ignore")
	ErrStop        = errors.New("stop")
	ErrUnsubscribe = errors.New("unsubscribe")
	ErrChar        = errors.New("unepected character")
	ErrMalformed   = errors.New("malformed document")
)

type NodeType rune

const (
	EOF NodeType = -(1 + iota)
	ProcInst
	BeginElement
	EndElement
	Text
	CData
	Comment
)

func (n NodeType) String() string {
	switch n {
	case ProcInst:
		return "processing-instruction"
	case BeginElement:
		return "begin-element"
	case EndElement:
		return "end-element"
	case Text:
		return "text"
	case CData:
		return "cdata"
	case Comment:
		return "comment"
	default:
		return "invalid"
	}
}

type Name struct {
	NS   string
	Name string
}

func (n Name) LocalName() string {
	return n.Name
}

func (n Name) Fqn() string {
	if n.NS == "" {
		return n.Name
	}
	return fmt.Sprintf("%s:%s", n.NS, n.Name)
}

func (n Name) String() string {
	return n.Fqn()
}

func (n Name) IsValid() bool {
	return n.Name != ""
}

func (n Name) Equal(other Name) bool {
	return n.NS == other.NS && n.Name == other.Name
}

type Node struct {
	Type NodeType

	Name
	Attrs       []Attr
	Content     string
	SelfClosing bool
}

type Attr struct {
	Name
	Value string
}

type KeepFunc func(NodeType, Name) error

func keepAll(_ NodeType, _ Name) error {
	return nil
}

type Reader struct {
	rs   *bufio.Reader
	last rune

	stack []Name
	keep  KeepFunc

	listeners struct {
		silent   bool
		begins   []func(Name) error
		ends     []func(Name) error
		insts    []func(Name) error
		attrs    []func(Name, string) error
		texts    []func(string) error
		comments []func(string) error
	}
}

func New(rs io.Reader, keep KeepFunc) *Reader {
	var r Reader
	r.rs = bufio.NewReader(rs)
	if keep == nil {
		keep = keepAll
	}
	r.keep = keep
	r.skipBlanks()
	return &r
}

func (r *Reader) Depth() int {
	return len(r.stack)
}

func (r *Reader) Read() (*Node, error) {
	for {
		n, err := r.next()
		if err != nil {
			return nil, err
		}
		switch err = r.keep(n.Type, n.Name); {
		case errors.Is(err, ErrIgnore):
			err := r.skipSubtree(n)
			if err != nil {
				return nil, err
			}
		case errors.Is(err, ErrSkip):
		default:
			return n, err
		}
	}
}

func (r *Reader) skipSubtree(n *Node) error {
	if n.Type != BeginElement || n.SelfClosing {
		return nil
	}
	r.silent()
	defer r.silent()
	depth := r.Depth()
	for {
		c, err := r.next()
		if err != nil {
			return err
		}
		if r.Depth() == depth && n.Name.Equal(c.Name) {
			break
		}
	}
	return nil
}

func (r *Reader) Run() error {
	for {
		_, err := r.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return err
		}
	}
}

func (r *Reader) OnBeginElement(fn func(Name) error) {
	r.listeners.begins = append(r.listeners.begins, fn)
}

func (r *Reader) OnEndElement(fn func(Name) error) {
	r.listeners.ends = append(r.listeners.ends, fn)
}

func (r *Reader) OnInstruction(fn func(Name) error) {
	r.listeners.insts = append(r.listeners.insts, fn)
}

func (r *Reader) OnAttribute(fn func(Name, string) error) {
	r.listeners.attrs = append(r.listeners.attrs, fn)
}

func (r *Reader) OnText(fn func(string) error) {
	r.listeners.texts = append(r.listeners.texts, fn)
}

func (r *Reader) OnComment(fn func(string) error) {
	r.listeners.comments = append(r.listeners.comments, fn)
}

func (r *Reader) silent() {
	r.listeners.silent = !r.listeners.silent
}

func (r *Reader) next() (*Node, error) {
	c, err := r.read()
	if err != nil {
		return nil, err
	}
	if c == langle {
		return r.parseNode()
	}
	r.unread()
	return r.parseText()
}

func (r *Reader) push(n *Node) {
	if n.SelfClosing {
		return
	}
	r.stack = append(r.stack, n.Name)
}

func (r *Reader) pop(n *Node) error {
	z := len(r.stack)
	if z == 0 {
		return fmt.Errorf("stack is empty")
	}
	pop := r.stack[z-1]
	if !pop.Equal(n.Name) {
		return fmt.Errorf("%w: element mismatched %s vs %s", ErrMalformed, pop.Name, n.Name.Name)
	}
	r.stack = r.stack[:z-1]
	return nil
}

func (r *Reader) parseNode() (*Node, error) {
	c, err := r.read()
	if err != nil {
		return nil, err
	}
	var n *Node
	switch {
	case c == mark:
		n, err = r.parseInstruction()
	case c == bang:
		c, err = r.read()
		r.unread()
		if c == lsquare {
			n, err = r.parseData()
		} else if c == hyphen {
			n, err = r.parseComment()
		} else {
			err = r.unexpectedChar(c)
		}
	case c == slash:
		n, err = r.parseEndElement()
		if err == nil {
			err = r.pop(n)
		}
	case isLetter(c):
		r.unread()
		n, err = r.parseOpenElement()
		if err == nil {
			r.push(n)
		}
	default:
		err = r.unexpectedChar(c)
	}
	r.skipBlanks()
	return n, err
}

func (r *Reader) parseData() (*Node, error) {
	if err := r.want(lsquare); err != nil {
		return nil, err
	}
	var (
		n   Node
		buf bytes.Buffer
		err error
	)
	n.Type = CData
	n.SelfClosing = true
	if n.Name, err = r.parseName(); err != nil {
		return nil, err
	}
	if n.Name.Name != "CDATA" {
		return nil, fmt.Errorf("%w: unexpected %s! want CDATA", ErrMalformed, n.Name)
	}
	if err := r.want(lsquare); err != nil {
		return nil, err
	}
	r.skipBlanks()
	for {
		c, err := r.read()
		if err != nil {
			return nil, err
		}
		if c == rsquare && r.peek() == c {
			r.read()
			if c, _ = r.read(); c == rangle {
				break
			}
			return nil, fmt.Errorf("%w: ]] can not appear in CDATA sections", ErrMalformed)
		}
		buf.WriteRune(c)
	}
	n.Content = strings.TrimSpace(buf.String())
	if err := r.emitText(n.Content); err != nil {
		return nil, err
	}
	return &n, nil
}

func (r *Reader) parseComment() (*Node, error) {
	if c, _ := r.read(); c == hyphen && r.peek() == c {
		r.read()
	} else {
		return nil, r.unexpectedChar(c)
	}
	r.skipBlanks()
	var (
		n   Node
		buf bytes.Buffer
	)
	n.SelfClosing = true
	n.Type = Comment
	for {
		c, err := r.read()
		if err != nil {
			return nil, err
		}
		if c == hyphen && r.peek() == c {
			r.read()
			if c, _ = r.read(); c == rangle {
				break
			}
			buf.WriteRune(hyphen)
			buf.WriteRune(hyphen)
		}
		if c == ampersand {
			c, err = r.parseEntity()
			if err != nil {
				return nil, err
			}
		}
		buf.WriteRune(c)
	}
	n.Content = strings.TrimSpace(buf.String())
	if err := r.emitComment(n.Content); err != nil {
		return nil, err
	}
	return &n, nil
}

func (r *Reader) parseText() (*Node, error) {
	var (
		n   Node
		buf bytes.Buffer
	)
	n.Type = Text
	for {
		c, err := r.read()
		if err != nil {
			return nil, err
		}
		if c == langle {
			break
		}
		if c == ampersand {
			c, err = r.parseEntity()
			if err != nil {
				return nil, err
			}
		}
		buf.WriteRune(c)
	}
	n.Content = strings.TrimSpace(buf.String())
	if err := r.emitText(n.Content); err != nil {
		return nil, err
	}
	return &n, r.unread()
}

func (r *Reader) parseInstruction() (*Node, error) {
	var (
		n   Node
		err error
	)
	n.SelfClosing = true
	n.Type = ProcInst
	if n.Name, err = r.parseName(); err != nil {
		return nil, err
	}
	if err := r.emitInst(n.Name); err != nil {
		return nil, err
	}
	r.skipBlanks()
	if err := r.parseAttributes(&n); err != nil {
		return nil, err
	}
	if err := r.want(mark); err != nil {
		return nil, err
	}
	return &n, r.want(rangle)
}

func (r *Reader) parseEndElement() (*Node, error) {
	var (
		n   Node
		err error
	)
	n.Type = EndElement
	if n.Name, err = r.parseName(); err != nil {
		return nil, err
	}
	if err := r.emitEnd(n.Name); err != nil {
		return nil, err
	}
	r.skipBlanks()
	return &n, r.want(rangle)
}

func (r *Reader) parseOpenElement() (*Node, error) {
	var (
		n   Node
		err error
	)
	n.Type = BeginElement
	if n.Name, err = r.parseName(); err != nil {
		return nil, err
	}
	if err := r.emitBegin(n.Name); err != nil {
		return nil, err
	}
	r.skipBlanks()
	if err := r.parseAttributes(&n); err != nil {
		return nil, err
	}
	c, err := r.read()
	if err != nil || c == rangle {
		return &n, err
	}
	if c != slash {
		return nil, r.unexpectedChar(c)
	}
	n.SelfClosing = true
	return &n, r.want(rangle)
}

func (r *Reader) parseName() (Name, error) {
	parse := func() (string, error) {
		c, err := r.read()
		if err != nil {
			return "", err
		}
		if !isLetter(c) {
			return "", fmt.Errorf("%w: name should start with a letter!", r.unexpectedChar(c))
		}
		var buf bytes.Buffer
		buf.WriteRune(c)
		for {
			if c, err = r.read(); err != nil {
				return "", err
			}
			if !isName(c) {
				break
			}
			buf.WriteRune(c)
		}
		return buf.String(), r.unread()
	}
	var (
		n   Name
		err error
	)
	if n.Name, err = parse(); err != nil {
		return n, err
	}
	if c := r.peek(); c != colon {
		return n, nil
	}
	n.NS, n.Name = n.Name, ""
	r.read()
	n.Name, err = parse()
	return n, err
}

func (r *Reader) parseValue() (string, error) {
	c, err := r.read()
	if err != nil {
		return "", err
	}
	if !isQuote(c) {
		return "", r.unexpectedChar(c)
	}
	var (
		buf   bytes.Buffer
		quote = c
	)
	for {
		if c, err = r.read(); err != nil {
			return "", err
		}
		if c == quote {
			break
		}
		if c == ampersand {
			c, err = r.parseEntity()
			if err != nil {
				return "", err
			}
		}
		buf.WriteRune(c)
	}
	return strings.TrimSpace(buf.String()), nil
}

func (r *Reader) parseAttributes(n *Node) error {
	seen := make(map[Name]struct{})
	for {
		c, err := r.read()
		if err != nil {
			return err
		}
		if !isName(c) {
			break
		}
		r.unread()
		var a Attr
		if a.Name, err = r.parseName(); err != nil {
			return err
		}
		if _, ok := seen[a.Name]; ok {
			return fmt.Errorf("%w: %s duplicated attribute", ErrMalformed, a.Name)
		}
		seen[a.Name] = struct{}{}
		r.skipBlanks()
		if err := r.want(equal); err != nil {
			return err
		}
		r.skipBlanks()
		if a.Value, err = r.parseValue(); err != nil {
			return err
		}
		n.Attrs = append(n.Attrs, a)
		if err := r.emitAttr(a.Name, a.Value); err != nil {
			return err
		}
		r.skipBlanks()
	}
	return r.unread()
}

var entities = map[string]rune{
	"quot": dquote,
	"apos": squote,
	"lt":   langle,
	"gt":   rangle,
	"amp":  ampersand,
}

const (
	baseDec = 10
	baseHex = 16
)

func (r *Reader) parseEntity() (rune, error) {
	c, err := r.read()
	if err != nil {
		return 0, err
	}
	if c == pound {
		c, err = r.read()
		if err != nil {
			return 0, err
		}
		var (
			accept = isDigit
			base   = baseDec
		)
		if c == 'x' {
			accept = isHex
			base = baseHex
		}
		return r.parseNumericEntity(base, accept)
	}
	return r.parseStringEntity()
}

func (r *Reader) parseStringEntity() (rune, error) {
	r.unread()

	var buf bytes.Buffer
	for {
		c, err := r.read()
		if err != nil {
			return 0, err
		}
		if c == semicolon {
			break
		}
		if !isLetter(c) {
			return 0, r.unexpectedChar(c)
		}
		buf.WriteRune(c)
	}
	c, ok := entities[buf.String()]
	if !ok {
		return 0, fmt.Errorf("%w: %s unknown entity", ErrMalformed, buf.String())
	}
	return c, nil
}

func (r *Reader) parseNumericEntity(base int, accept func(rune) bool) (rune, error) {
	var buf bytes.Buffer
	for {
		c, err := r.read()
		if err != nil {
			return 0, err
		}
		if c == semicolon {
			break
		}
		if !accept(c) {
			return 0, r.unexpectedChar(c)
		}
		buf.WriteRune(c)
	}
	n, err := strconv.ParseInt(buf.String(), base, 32)
	return rune(n), err
}

func (r *Reader) skipBlanks() {
	defer r.unread()
	for {
		c, err := r.read()
		if err != nil || !isBlank(c) {
			break
		}
	}
}

func (r *Reader) want(char rune) error {
	return r.wantFunc(func(r rune) bool { return r == char })
}

func (r *Reader) wantFunc(fn func(rune) bool) error {
	c, err := r.read()
	if err != nil {
		return err
	}
	ok := fn(c)
	if !ok {
		return r.unexpectedChar(c)
	}
	return nil
}

func (r *Reader) emitBegin(n Name) error {
	var err error
	if r.listeners.silent {
		return err
	}
	r.listeners.begins, err = r.emitNode(n, r.listeners.begins)
	return err
}

func (r *Reader) emitEnd(n Name) error {
	var err error
	if r.listeners.silent {
		return err
	}
	r.listeners.ends, err = r.emitNode(n, r.listeners.ends)
	return err
}

func (r *Reader) emitInst(n Name) error {
	var err error
	if r.listeners.silent {
		return err
	}
	r.listeners.insts, err = r.emitNode(n, r.listeners.insts)
	return err
}

func (r *Reader) emitText(str string) error {
	var err error
	if r.listeners.silent {
		return err
	}
	r.listeners.texts, err = r.emitString(str, r.listeners.texts)
	return err
}

func (r *Reader) emitComment(str string) error {
	var err error
	if r.listeners.silent {
		return err
	}
	r.listeners.comments, err = r.emitString(str, r.listeners.comments)
	return err
}

func (r *Reader) emitAttr(n Name, str string) error {
	if r.listeners.silent {
		return nil
	}
	for i := 0; i < len(r.listeners.attrs); i++ {
		fn := r.listeners.attrs[i]
		if err := fn(n, str); err != nil {
			if errors.Is(err, ErrUnsubscribe) {
				r.listeners.attrs = append(r.listeners.attrs[:i], r.listeners.attrs[i+1:]...)
				i--
				continue
			}
			return checkListenerError(err)
		}
	}
	return nil
}

func (r *Reader) emitString(str string, set []func(string) error) ([]func(string) error, error) {
	for i := 0; i < len(set); i++ {
		fn := set[i]
		if err := fn(str); err != nil {
			if errors.Is(err, ErrUnsubscribe) {
				set = append(set[:i], set[i+1:]...)
				i--
				continue
			}
			return set, checkListenerError(err)
		}
	}
	return set, nil
}

func (r *Reader) emitNode(n Name, set []func(Name) error) ([]func(Name) error, error) {
	for i := 0; i < len(set); i++ {
		fn := set[i]
		if err := fn(n); err != nil {
			if errors.Is(err, ErrUnsubscribe) {
				set = append(set[:i], set[i+1:]...)
				i--
				continue
			}
			return set, checkListenerError(err)
		}
	}
	return set, nil
}

func (r *Reader) read() (rune, error) {
	c, _, err := r.rs.ReadRune()
	return c, err
}

func (r *Reader) unread() error {
	return r.rs.UnreadRune()
}

func (r *Reader) peek() rune {
	defer r.unread()
	c, _ := r.read()
	return c
}

func (r *Reader) unexpectedChar(c rune) error {
	return fmt.Errorf("%c: %w", c, ErrChar)
}

func checkListenerError(err error) error {
	if errors.Is(err, ErrStop) {
		return nil
	}
	return err
}

func isName(r rune) bool {
	return isLetter(r) || isDigit(r) || r == hyphen || r == underscore
}

func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

func isHex(r rune) bool {
	return isDigit(r) || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func isQuote(r rune) bool {
	return r == dquote || r == squote
}

func isBlank(r rune) bool {
	return isSpace(r) || isNL(r)
}

func isSpace(r rune) bool {
	return r == space || r == tab
}

func isNL(r rune) bool {
	return r == nl || r == cr
}
