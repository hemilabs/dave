// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/template"
	"unicode/utf8"
)

// Printer is used for printing things to the terminal.
// This should be used for all logging in the CLI.
type Printer struct {
	w io.Writer
	v int
}

// TODO: If we're adding progress bars, we may need a mutex in the printer to
//  avoid erasing things we don't want to.
//
// Note: If we need to erase lines (e.g. for progress bars), make sure it is
//  cross-platform compatible because certain platforms (*cough* windows)
//  behave weirdly and don't respect standards :)

func newPrinter(w io.Writer, v int) *Printer {
	return &Printer{w: w, v: v}
}

func (p *Printer) SetVerbosity(level int) {
	p.v = level + 1
}

func (p *Printer) SetQuiet(quiet bool) {
	if quiet {
		p.v = 0
	}
}

// Ef prints an error message. It is always printed.
func (p *Printer) Ef(msg string, args ...any) {
	_, _ = fmt.Fprintf(p.w, msg, args...)
}

// Pf prints a message if the verbosity >= 1.
// This should be used for informational/progress messages.
func (p *Printer) Pf(msg string, args ...any) {
	if p.v >= 1 {
		_, _ = fmt.Fprintf(p.w, msg, args...)
	}
}

// Vf prints a message if verbosity >= 2.
// This should be used for verbose messages.
func (p *Printer) Vf(msg string, args ...any) {
	if p.v >= 2 {
		_, _ = fmt.Fprintf(p.w, msg, args...)
	}
}

// VVf prints a message if verbosity >= 3.
// This should be used for debug messages.
func (p *Printer) VVf(msg string, args ...any) {
	if p.v >= 3 {
		_, _ = fmt.Fprintf(p.w, msg, args...)
	}
}

// verbosityToLevel returns an slog.Level for the current verbosity.
func verbosityToLevel(v int) slog.Level {
	switch {
	case v >= 2:
		return slog.LevelDebug
	case v >= 1:
		return slog.LevelInfo
	case v >= 0:
		return slog.LevelWarn
	default:
		return slog.LevelError
	}
}

// table is used to render aligned tables.
type table struct {
	headers   []string
	templates []*template.Template
	rows      []any
	footer    []string

	Separator string
}

var funcMap = template.FuncMap{
	"join": strings.Join,
}

func newTable() *table {
	return &table{
		Separator: "  ",
	}
}

func (t *table) AddColumn(header, format string) {
	t.headers = append(t.headers, header)
	tmpl := template.Must(template.New(header).Funcs(funcMap).Parse(format))
	t.templates = append(t.templates, tmpl)
}

func (t *table) AddRow(row any) {
	t.rows = append(t.rows, row)
}

func (t *table) AddRows(rows []any) {
	t.rows = append(t.rows, rows...)
}

func (t *table) AddFooter(s string) {
	t.footer = append(t.footer, s)
}

func (t *table) Render(w io.Writer) error {
	columns := len(t.templates)
	if columns == 0 {
		// Nothing to do.
		return nil
	}

	// Render all row columns.
	rows := make([][]string, 0, len(t.rows))
	buf := bytes.NewBuffer(nil)
	for _, row := range t.rows {
		cols := make([]string, 0, len(t.templates))
		for _, tmpl := range t.templates {
			if err := tmpl.Execute(buf, row); err != nil {
				return err
			}
			cols = append(cols, buf.String())
			buf.Reset()
		}
		rows = append(rows, cols)
	}

	// Determine max column widths.
	colWidths := make([]int, columns)
	for i, h := range t.headers {
		colWidths[i] = printWidth(h)
	}
	for _, row := range rows {
		for i, col := range row {
			width := printWidth(col)
			if width > colWidths[i] {
				colWidths[i] = width
			}
		}
	}

	var totalWidth int
	for _, width := range colWidths {
		totalWidth += width
	}
	totalWidth += printWidth(t.Separator) * (columns - 1)

	// Print header
	if len(t.headers) > 0 {
		// Write header line
		if err := printLine(w, t.Separator, t.headers, colWidths); err != nil {
			return err
		}
		// Write separator line
		if err := printSeparatorLine(w, totalWidth); err != nil {
			return err
		}
	}

	for _, row := range rows {
		if err := printLine(w, t.Separator, row, colWidths); err != nil {
			return err
		}
	}

	// Write separator line
	if len(t.headers) > 0 || len(t.footer) > 0 {
		if err := printSeparatorLine(w, totalWidth); err != nil {
			return err
		}
	}

	// Write footer
	for _, l := range t.footer {
		if _, err := io.WriteString(w, l+"\n"); err != nil {
			return err
		}
	}

	return nil
}

func printSeparatorLine(w io.Writer, width int) error {
	_, err := io.WriteString(w, strings.Repeat("-", width)+"\n")
	return err
}

func printLine(w io.Writer, sep string, columns []string, columnWidths []int) error {
	maxLines := 1
	fields := make([][]string, len(columns))
	for i, col := range columns {
		l := strings.Split(col, "\n")
		if ln := len(l); ln > maxLines {
			maxLines = ln
		}
		fields[i] = l
	}

	for i := range maxLines {
		var s string
		for n, lines := range fields {
			var v string
			if i < len(lines) {
				v += lines[i]
			}

			pad := columnWidths[n] - printWidth(v)
			if pad > 0 {
				v += strings.Repeat(" ", pad)
			}
			if n > 0 {
				v = sep + v
			}
			s += v
		}

		if _, err := w.Write([]byte(s + "\n")); err != nil {
			return err
		}
	}

	return nil
}

func printWidth(s string) int {
	var w, l int
	for _, line := range strings.Split(s, "\n") {
		for _, r := range line {
			l += utf8.RuneLen(r)
		}
		if l > w {
			w = l
		}
		l = 0
	}
	return w
}
