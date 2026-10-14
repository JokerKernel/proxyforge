package system

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

const detailLabelWidth = 22

// DetailPrefix aligns operation labels by terminal display width, including
// Chinese characters. A prefix can also be used for streaming command output.
func DetailPrefix(label string) string {
	padding := detailLabelWidth - DisplayWidth(label)
	if padding < 0 {
		padding = 0
	}
	return label + strings.Repeat(" ", padding) + " │ "
}

// DetailLine keeps complete details, indenting continuation lines under the
// right column. It does not add color or alter data written outside this API.
func DetailLine(label, detail string) string {
	prefix := DetailPrefix(label)
	width := max(detailLabelWidth, DisplayWidth(label))
	continuation := strings.Repeat(" ", width) + " │ "
	return prefix + strings.ReplaceAll(detail, "\n", "\n"+continuation) + "\n"
}

func WriteDetail(output io.Writer, label, format string, args ...any) {
	if output != nil {
		fmt.Fprint(output, DetailLine(label, fmt.Sprintf(format, args...)))
	}
}

func DisplayWidth(value string) int {
	width := 0
	for _, r := range value {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.IsControl(r) {
			continue
		}
		if IsWideRune(r) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

func IsWideRune(r rune) bool {
	return r >= 0x1100 && (r <= 0x115f ||
		r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1faff) ||
		(r >= 0x20000 && r <= 0x3fffd))
}
