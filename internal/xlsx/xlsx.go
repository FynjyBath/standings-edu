// Package xlsx — минимальный генератор .xlsx (без зависимостей): книга из
// нескольких листов с текстом, числами и формулами, объединением ячеек и
// шириной колонок. Формат совместим с Excel и импортом в Google Таблицы.
// Строки — inline (без sharedStrings), формулы без кэшированных значений:
// в workbook.xml включён fullCalcOnLoad, чтобы Excel пересчитал при открытии
// (Google Таблицы пересчитывают всегда).
package xlsx

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// CellKind — тип содержимого ячейки.
type CellKind int

const (
	CellEmpty   CellKind = iota
	CellText             // текст как есть
	CellNumber           // Value — число в десятичной записи
	CellFormula          // Value — формула без ведущего «=»
)

// StyleID — индекс стиля в фиксированной таблице стилей книги.
type StyleID int

const (
	StyleDefault StyleID = iota
	StyleHeader          // жирный, по центру, перенос строк
	StyleMuted           // серый (второстепенное: веса, подписи)
)

type Cell struct {
	Kind  CellKind
	Value string
	Style StyleID
	// NumFmt — кастомный числовой формат (код Excel, напр. `0" / 24"`):
	// значение остаётся числом для формул, показывается с подписью.
	NumFmt string
}

// CondFmt — условное форматирование диапазона: либо правило «текст равен»
// (готовые стили «хорошо»/«плохо»), либо цветовая шкала по числам.
type CondFmt struct {
	Sqref string // диапазон, напр. "D3:F12"
	// Текстовое правило: ячейка равна Text → зелёный (Good) или красный фон.
	Text string
	Good bool
	// Шкала: градиент от Min к Max цветами Colors (2 или 3 значений RRGGBB).
	Scale    bool
	Min, Max float64
	Colors   []string
}

func Text(s string) Cell        { return Cell{Kind: CellText, Value: s} }
func Header(s string) Cell      { return Cell{Kind: CellText, Value: s, Style: StyleHeader} }
func Muted(s string) Cell       { return Cell{Kind: CellText, Value: s, Style: StyleMuted} }
func Number(v string) Cell      { return Cell{Kind: CellNumber, Value: v} }
func Formula(f string) Cell     { return Cell{Kind: CellFormula, Value: f} }
func MutedNumber(v string) Cell { return Cell{Kind: CellNumber, Value: v, Style: StyleMuted} }

// Sheet — один лист: строки ячеек (разреженность — через CellEmpty), диапазоны
// объединения ("A1:C2") и ширины колонок (0-based индекс → ширина в символах).
type Sheet struct {
	Name      string
	Rows      [][]Cell
	Merges    []string
	ColWidths map[int]float64
	CondFmts  []CondFmt
	// FreezeRows/FreezeCols — закрепить первые строки/колонки.
	FreezeRows int
	FreezeCols int
}

type Workbook struct {
	Sheets []*Sheet
}

// SetName переименовывает лист (имя проходит ту же чистку, что при создании).
func (s *Sheet) SetName(name string) { s.Name = sanitizeSheetName(name) }

func (w *Workbook) AddSheet(name string) *Sheet {
	s := &Sheet{Name: sanitizeSheetName(name), ColWidths: map[int]float64{}}
	w.Sheets = append(w.Sheets, s)
	return s
}

// ColName — имя колонки по 0-based индексу: 0→A, 25→Z, 26→AA.
func ColName(i int) string {
	name := ""
	for i >= 0 {
		name = string(rune('A'+i%26)) + name
		i = i/26 - 1
	}
	return name
}

// CellRef — адрес ячейки по 0-based колонке и строке: (0,0)→A1.
func CellRef(col, row int) string {
	return fmt.Sprintf("%s%d", ColName(col), row+1)
}

var badSheetChars = regexp.MustCompile(`[\\/?*\[\]:]`)

func sanitizeSheetName(name string) string {
	name = badSheetChars.ReplaceAllString(strings.TrimSpace(name), " ")
	if name == "" {
		name = "Лист"
	}
	runes := []rune(name)
	if len(runes) > 31 {
		runes = runes[:31]
	}
	return string(runes)
}

func esc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Write пишет книгу в w как готовый .xlsx.
func (wb *Workbook) Write(w io.Writer) error {
	z := zip.NewWriter(w)
	add := func(name, body string) error {
		f, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, body)
		return err
	}

	var types strings.Builder
	types.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	for i := range wb.Sheets {
		fmt.Fprintf(&types, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i+1)
	}
	types.WriteString(`</Types>`)
	if err := add("[Content_Types].xml", types.String()); err != nil {
		return err
	}

	if err := add("_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`); err != nil {
		return err
	}

	var book, rels strings.Builder
	book.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	rels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i, s := range wb.Sheets {
		fmt.Fprintf(&book, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, esc(s.Name), i+1, i+1)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
	}
	fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, len(wb.Sheets)+1)
	rels.WriteString(`</Relationships>`)
	// fullCalcOnLoad: формулы без кэша значений пересчитываются при открытии.
	book.WriteString(`</sheets><calcPr fullCalcOnLoad="1"/></workbook>`)
	if err := add("xl/workbook.xml", book.String()); err != nil {
		return err
	}
	if err := add("xl/_rels/workbook.xml.rels", rels.String()); err != nil {
		return err
	}

	// Кастомные числовые форматы: собираем со всех ячеек; каждой паре
	// (базовый стиль, формат) — свой xf. Базовые xf 0..2 всегда на месте.
	numFmtID := map[string]int{}
	xfIndex := map[[2]interface{}]int{} // {StyleID, numFmt} → индекс xf
	type xfDef struct {
		style StyleID
		fmtID int
	}
	extraXfs := []xfDef{}
	nextFmt := 164
	for _, s := range wb.Sheets {
		for _, row := range s.Rows {
			for _, c := range row {
				if c.NumFmt == "" {
					continue
				}
				id, ok := numFmtID[c.NumFmt]
				if !ok {
					id = nextFmt
					nextFmt++
					numFmtID[c.NumFmt] = id
				}
				key := [2]interface{}{c.Style, c.NumFmt}
				if _, ok := xfIndex[key]; !ok {
					xfIndex[key] = 3 + len(extraXfs)
					extraXfs = append(extraXfs, xfDef{style: c.Style, fmtID: id})
				}
			}
		}
	}
	resolveXf := func(c Cell) int {
		if c.NumFmt == "" {
			return int(c.Style)
		}
		return xfIndex[[2]interface{}{c.Style, c.NumFmt}]
	}

	// Стили: 0 — обычный; 1 — жирный по центру с переносом; 2 — серый.
	// dxf 0/1 — «хорошо»/«плохо» для условного форматирования (палитра сайта).
	var st strings.Builder
	st.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	if len(numFmtID) > 0 {
		fmt.Fprintf(&st, `<numFmts count="%d">`, len(numFmtID))
		fmts := make([]string, 0, len(numFmtID))
		for f := range numFmtID {
			fmts = append(fmts, f)
		}
		sortStrings(fmts, numFmtID)
		for _, f := range fmts {
			fmt.Fprintf(&st, `<numFmt numFmtId="%d" formatCode="%s"/>`, numFmtID[f], esc(f))
		}
		st.WriteString(`</numFmts>`)
	}
	st.WriteString(`<fonts count="3"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font><font><sz val="10"/><color rgb="FF808080"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>`)
	fmt.Fprintf(&st, `<cellXfs count="%d">`, 3+len(extraXfs))
	st.WriteString(`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyAlignment="1"><alignment horizontal="center" vertical="center" wrapText="1"/></xf><xf numFmtId="0" fontId="2" fillId="0" borderId="0" xfId="0"/>`)
	for _, xf := range extraXfs {
		fontID := 0
		if xf.style == StyleHeader {
			fontID = 1
		} else if xf.style == StyleMuted {
			fontID = 2
		}
		fmt.Fprintf(&st, `<xf numFmtId="%d" fontId="%d" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>`, xf.fmtID, fontID)
	}
	st.WriteString(`</cellXfs>`)
	st.WriteString(`<dxfs count="2"><dxf><font><color rgb="FF123622"/></font><fill><patternFill><bgColor rgb="FF92D8AA"/></patternFill></fill></dxf><dxf><font><color rgb="FF7B2323"/></font><fill><patternFill><bgColor rgb="FFF8D7D7"/></patternFill></fill></dxf></dxfs>`)
	st.WriteString(`</styleSheet>`)
	if err := add("xl/styles.xml", st.String()); err != nil {
		return err
	}

	for i, s := range wb.Sheets {
		if err := add(fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), sheetXML(s, resolveXf)); err != nil {
			return err
		}
	}
	return z.Close()
}

// sortStrings сортирует форматы по их id — стабильный порядок в styles.xml.
func sortStrings(fmts []string, ids map[string]int) {
	for i := 1; i < len(fmts); i++ {
		for j := i; j > 0 && ids[fmts[j-1]] > ids[fmts[j]]; j-- {
			fmts[j-1], fmts[j] = fmts[j], fmts[j-1]
		}
	}
}

func sheetXML(s *Sheet, resolveXf func(Cell) int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)

	if s.FreezeRows > 0 || s.FreezeCols > 0 {
		top := CellRef(s.FreezeCols, s.FreezeRows)
		b.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane`)
		if s.FreezeCols > 0 {
			fmt.Fprintf(&b, ` xSplit="%d"`, s.FreezeCols)
		}
		if s.FreezeRows > 0 {
			fmt.Fprintf(&b, ` ySplit="%d"`, s.FreezeRows)
		}
		fmt.Fprintf(&b, ` topLeftCell="%s" state="frozen"/></sheetView></sheetViews>`, top)
	}

	if len(s.ColWidths) > 0 {
		b.WriteString(`<cols>`)
		// Стабильный порядок: по возрастанию индекса.
		maxCol := 0
		for c := range s.ColWidths {
			if c > maxCol {
				maxCol = c
			}
		}
		for c := 0; c <= maxCol; c++ {
			if wd, ok := s.ColWidths[c]; ok {
				fmt.Fprintf(&b, `<col min="%d" max="%d" width="%g" customWidth="1"/>`, c+1, c+1, wd)
			}
		}
		b.WriteString(`</cols>`)
	}

	b.WriteString(`<sheetData>`)
	for ri, row := range s.Rows {
		fmt.Fprintf(&b, `<row r="%d">`, ri+1)
		for ci, cell := range row {
			if cell.Kind == CellEmpty && cell.Style == StyleDefault && cell.NumFmt == "" {
				continue
			}
			ref := CellRef(ci, ri)
			style := ""
			if xf := resolveXf(cell); xf != 0 {
				style = fmt.Sprintf(` s="%d"`, xf)
			}
			switch cell.Kind {
			case CellText:
				fmt.Fprintf(&b, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, esc(cell.Value))
			case CellNumber:
				fmt.Fprintf(&b, `<c r="%s"%s><v>%s</v></c>`, ref, style, esc(cell.Value))
			case CellFormula:
				fmt.Fprintf(&b, `<c r="%s"%s><f>%s</f></c>`, ref, style, esc(cell.Value))
			default:
				fmt.Fprintf(&b, `<c r="%s"%s/>`, ref, style)
			}
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData>`)

	if len(s.Merges) > 0 {
		fmt.Fprintf(&b, `<mergeCells count="%d">`, len(s.Merges))
		for _, m := range s.Merges {
			fmt.Fprintf(&b, `<mergeCell ref="%s"/>`, m)
		}
		b.WriteString(`</mergeCells>`)
	}

	// Условное форматирование: текстовые правила ссылаются на dxf 0 («хорошо»,
	// зелёный) и 1 («плохо», красный); шкалы — градиент по числам.
	prio := 1
	for _, cf := range s.CondFmts {
		fmt.Fprintf(&b, `<conditionalFormatting sqref="%s">`, esc(cf.Sqref))
		if cf.Scale {
			fmt.Fprintf(&b, `<cfRule type="colorScale" priority="%d"><colorScale>`, prio)
			switch len(cf.Colors) {
			case 3:
				fmt.Fprintf(&b, `<cfvo type="num" val="%g"/><cfvo type="num" val="%g"/><cfvo type="num" val="%g"/>`, cf.Min, (cf.Min+cf.Max)/2, cf.Max)
			default:
				fmt.Fprintf(&b, `<cfvo type="num" val="%g"/><cfvo type="num" val="%g"/>`, cf.Min, cf.Max)
			}
			for _, c := range cf.Colors {
				fmt.Fprintf(&b, `<color rgb="FF%s"/>`, esc(c))
			}
			b.WriteString(`</colorScale></cfRule>`)
		} else {
			dxf := 1
			if cf.Good {
				dxf = 0
			}
			fmt.Fprintf(&b, `<cfRule type="cellIs" dxfId="%d" priority="%d" operator="equal"><formula>"%s"</formula></cfRule>`, dxf, prio, esc(cf.Text))
		}
		b.WriteString(`</conditionalFormatting>`)
		prio++
	}
	b.WriteString(`</worksheet>`)
	return b.String()
}
