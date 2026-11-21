package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

type nameEncoding uint32

var encodingNames = []string{
	"Raw", "Slash", "LtGt", "DoubleQuote", "SingleQuote", "BackQuote", "Dollar", "Colon", "Question", "Asterisk", "Pipe",
	"Hash", "Percent", "BackSlash", "CrLf", "Del", "Ctl", "LeftSpace", "LeftPeriod", "LeftTilde", "LeftCrLfHtVt",
	"RightSpace", "RightPeriod", "RightCrLfHtVt", "InvalidUtf8", "Dot", "SquareBracket", "Semicolon", "Exclamation",
}

const quoteRune = '‛'
const fullWidth = 0xfee0
const standardEncoding = nameEncoding(1<<1 | 1<<15 | 1<<16 | 1<<25)

func parseEncoding(s string) (nameEncoding, error) {
	var mask nameEncoding
	for _, tag := range strings.Split(s, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "None" {
			continue
		}
		known := false
		for i, name := range encodingNames {
			if tag == name {
				mask |= 1 << i
				known = true
				break
			}
		}
		if !known {
			n, err := strconv.ParseUint(tag, 0, 32)
			if err != nil {
				return 0, fmt.Errorf("encoding desconocido: %q", tag)
			}
			mask |= nameEncoding(n)
		}
	}
	return mask, nil
}

func (e nameEncoding) windows() nameEncoding {
	return (e &^ 1) | 1<<1 | 1<<2 | 1<<3 | 1<<7 | 1<<8 | 1<<9 | 1<<10 | 1<<13 | 1<<16 | 1<<21 | 1<<22 | 1<<24 | 1<<25
}

var translations = []struct {
	chars string
	flag  uint
}{
	{"/", 1}, {"<>", 2}, {"\"", 3}, {"'", 4}, {"`", 5}, {"$", 6}, {":", 7}, {"?", 8}, {"*", 9}, {"|", 10},
	{"#", 11}, {"%", 12}, {"\\", 13}, {"[]", 26}, {";", 27}, {"!", 28},
}

func (e nameEncoding) replacement(ascii rune, left, right bool) rune {
	switch {
	case ascii == 0:
		return '␀'
	case ascii == ' ' && (left && e&(1<<17) != 0 || right && e&(1<<21) != 0):
		return '␠'
	case ascii == '.' && (left && e&(1<<18) != 0 || right && e&(1<<22) != 0):
		return '．'
	case ascii == '~' && left && e&(1<<19) != 0:
		return '～'
	case ascii > 0 && ascii < 32:
		all := e&(1<<16) != 0
		line := e&(1<<14) != 0 && (ascii == '\r' || ascii == '\n')
		edge := (left && e&(1<<20) != 0 || right && e&(1<<23) != 0) && strings.ContainsRune("\t\n\v\r", ascii)
		if all || line || edge {
			return '␀' + ascii
		}
	case ascii == 127 && e&(1<<15) != 0:
		return '␡'
	}
	for _, rule := range translations {
		if e&(1<<rule.flag) != 0 && strings.ContainsRune(rule.chars, ascii) {
			return ascii + fullWidth
		}
	}
	return 0
}

func (e nameEncoding) inverse(r rune, left, right bool) (rune, bool) {
	var ascii rune
	switch {
	case r >= '␀' && r <= '␠':
		ascii = r - '␀'
	case r == '␡':
		ascii = 127
	case r >= 0xff01 && r <= 0xff5e:
		ascii = r - fullWidth
	default:
		return 0, false
	}
	return ascii, e.replacement(ascii, left, right) == r
}

func (e nameEncoding) encode(s string) string {
	if e == 1 {
		return s
	}
	if e&(1<<25) != 0 {
		switch s {
		case ".":
			return "．"
		case "..":
			return "．．"
		case "．":
			return "‛．"
		case "．．":
			return "‛．‛．"
		}
	}
	var out strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		left, right := i == 0, i+n == len(s)
		if r == utf8.RuneError && n == 1 {
			if e&(1<<24) != 0 {
				fmt.Fprintf(&out, "%c%02X", quoteRune, s[i])
			} else {
				out.WriteByte(s[i])
			}
		} else if display := e.replacement(r, left, right); display != 0 {
			out.WriteRune(display)
		} else {
			if _, esc := e.inverse(r, left, right); esc || r == quoteRune {
				out.WriteRune(quoteRune)
			}
			out.WriteRune(r)
		}
		i += n
	}
	return out.String()
}

func (e nameEncoding) decode(s string) string {
	if e == 1 {
		return s
	}
	// rclone 1.75.2 interprets the last escape independently from a run of
	// preceding escapes when a trailing-character encoding is enabled.
	r, n := utf8.DecodeLastRuneInString(s)
	rightEdge := r == '␠' && e&(1<<21) != 0 || r == '．' && e&(1<<22) != 0 ||
		e&(1<<23) != 0 && strings.ContainsRune("␉␊␋␍", r)
	if rightEdge {
		rest := s[:len(s)-n]
		quoted := false
		for strings.HasSuffix(rest, string(quoteRune)) {
			quoted = !quoted
			rest = rest[:len(rest)-utf8.RuneLen(quoteRune)]
		}
		if !quoted && len(s)-n > len(rest) {
			return e.decodeName(s[:len(s)-n-utf8.RuneLen(quoteRune)]) + string(r)
		}
	}
	return e.decodeName(s)
}

func (e nameEncoding) decodeName(s string) string {
	if e == 1 {
		return s
	}
	if e&(1<<25) != 0 {
		switch s {
		case "．":
			return "."
		case "．．":
			return ".."
		case "‛．":
			return "．"
		case "‛．‛．":
			return "．．"
		}
	}
	var out strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		left, right := i == 0, i+n == len(s)
		if r == quoteRune && i+n < len(s) {
			next, k := utf8.DecodeRuneInString(s[i+n:])
			if _, esc := e.inverse(next, left, i+n+k == len(s)); esc || next == quoteRune {
				out.WriteRune(next)
				i += n + k
				continue
			}
			if e&(1<<24) != 0 && len(s)-i-n >= 2 {
				if value, err := strconv.ParseUint(s[i+n:i+n+2], 16, 8); err == nil {
					out.WriteByte(byte(value))
					i += n + 2
					continue
				}
			}
		}
		if ascii, ok := e.inverse(r, left, right); ok {
			out.WriteRune(ascii)
		} else {
			out.WriteString(s[i : i+n])
		}
		i += n
	}
	return out.String()
}
