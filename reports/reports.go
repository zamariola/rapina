package reports

import (
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/360EntSecGroup-Skylar/excelize"
	p "github.com/dude333/rapina/parsers"
	"github.com/pkg/errors"
)

const sectorAverage = "MÉDIA DO SETOR"

const (
	grpAccts int = iota + 100
	grpShares
	grpExtra
	grpFleuriet
)

// metric parameters
type metric struct {
	descr  string
	val    float32
	format int // mapped by constants NUMBER, INDEX, PERCENT
	group  int // mapped by constants grpX
}

// report parameters used in most functions
type report struct {
	// Sqlite3 handle passed by the caller
	db *sql.DB

	// yamlFile contains the sector data for all companies
	yamlFile string

	// average metric values/year. Index 0: year, index 1: metric
	average [][]float32

	// groups that will be printed on the output xlsx
	groups map[int]bool
}

//
// Report of company data from DB to Excel
//
func Report(p Parms) error {
	cid, err := cid(p.DB, p.Company)
	if err != nil {
		return fmt.Errorf("empresa '%s' não encontrada no banco de dados", p.Company)
	}

	// Initialize report object
	r := report{
		db:       p.DB,
		yamlFile: p.YamlFile,
	}
	r.groups = make(map[int]bool, 3)
	r.groups[grpAccts] = true
	r.groups[grpShares] = p.Reports["ShowShares"]
	r.groups[grpExtra] = p.Reports["ExtraRatios"]
	r.groups[grpFleuriet] = p.Reports["Fleuriet"]

	e := newExcel()
	sheet, _ := e.newSheet(p.Company)

	// Company name
	sheet.mergeCell("A1", "B1")
	sheet.print("A1", &[]string{p.Company}, LEFT, true)

	// ACCOUNT NUMBERING AND DESCRIPTION (COLS A AND B) ===============\/
	accounts, _ := r.accountsItems(cid)
	baseItems, lastStatementsRow, lastMetricsRow := r.printCodesAndDescriptions(sheet, accounts, 'A', 2)

	// 	VALUES (COLS C, D, E...) / PER YEAR ===========================\/

	begin, end, err := timeRange(r.db)
	if err != nil {
		return err
	}
	var values map[uint32]float32

	// LOOP THROUGH YEARS =============================================\/
	for y := begin; y <= end; y++ {
		// Title
		row := 2
		col := colLetter(2 + y - begin) // start on col 'C'
		cell := col + "1"
		title := "[" + strconv.Itoa(y) + "]"

		lastYear, isTTM, err := r.lastYear(cid)
		if lastYear == y && isTTM && err == nil {
			title = "[TTM/" + strconv.Itoa(y) + "]"
		}

		// ACCOUNT VALUES (COLS C, D, E...) / YEAR ====================\/
		values, err = r.accountsValues(cid, y)
		if err != nil {
			fmt.Println("[x]", err)
			continue
		}
		// Skip last year if empty
		if y == end && sum(values) == 0 {
			end--
			break
		}
		sheet.printTitle(cell, title) // Print year as title on row 1
		for _, acct := range accounts {
			cell := col + strconv.Itoa(row)
			sheet.printValue(cell, values[acct.code], NUMBER, baseItems[row])
			row++
		}

		// FINANCIAL METRICS (COLS C, D, E...) / YEAR =================\/
		row++
		cell = col + strconv.Itoa(row)
		sheet.printTitle(cell, title) // Print year as title
		row++
		// Print report in the sequence defined on metricsList()
		for _, metric := range metricsList(values) {
			if !r.groups[metric.group] {
				continue
			}
			if metric.format != EMPTY {
				cell := col + strconv.Itoa(row)
				sheet.printValue(cell, metric.val, metric.format, false)
			}
			row++
		}

	} // next year

	//
	// VERTICAL ANALYSIS
	//
	// CODES | DESCRIPTION | Y1 | Y2 | Yn | sp | v1 | v2 | v3
	//
	wide := (end - begin)
	year := begin
	top := 2
	bottom := top
	for col := 2; col <= 2+wide; col++ {
		vCol := col + wide + 2                                  // Column where the vertical analysis will be printed
		sheet.printTitle(axis(vCol, 1), "'"+strconv.Itoa(year)) // Print year
		year++
		var ref string
		for row := top; row <= lastStatementsRow; row++ {
			idx := row - top
			if idx < 0 || idx >= len(accounts) {
				break
			}
			if len(accounts[idx].cdConta) == 0 {
				break
			}
			n, _ := strconv.Atoi(accounts[idx].cdConta[:1])
			if n > 3 {
				break
			}
			switch accounts[idx].cdConta {
			case "1", "2", "3.01":
				ref = axis(col, row)
			}
			val := axis(col, row)
			formula := fmt.Sprintf(`=IfError(%s/%s, "-")`, val, ref)

			sheet.printFormula(axis(vCol, row), formula, PERCENT, baseItems[row])
			bottom = row
		}
	}

	// Print VERTICAL ANALYSIS title
	sheet.mergeCell(axis(1+wide+2, top), axis(1+wide+2, bottom))
	format := newFormat(DEFAULT, RIGHT, true)
	format.Alignment.Vertical = "top"
	format.Alignment.TextRotation = 90
	rotatedTextStyle := format.newStyle(sheet.xlsx)
	sheet.printCell(top, 1+wide+2, "ANÁLISE  VERTICAL", rotatedTextStyle)

	//
	// HORIZONTAL ANALYSIS
	//
	// sp | DESCRIPTION | Y1 | Y2 | Yn | sp | h1 | h2 | hn
	//
	wide = (end - begin)
	year = begin
	top = lastStatementsRow + 2
	bottom = lastMetricsRow
	for col := 0; col <= wide-1; col++ {
		year++
		vCol := (2 + wide + 2) + col                              // Column where the horizontal analysis will be printed
		sheet.printTitle(axis(vCol, top), "'"+strconv.Itoa(year)) // Print year
		for row := top + 1; row <= bottom; row++ {
			vt0 := axis(col+2, row)
			vtn := axis(col+3, row)
			formula := fmt.Sprintf(`=IF(OR(%s="", %s=""), "", IF(MIN(%s, %s)<=0, IF((%s - %s)>0, "      ⇧", "      ⇩"), (%s/%s)-1))`,
				vtn, vt0, vtn, vt0, vtn, vt0, vtn, vt0)
			sheet.printFormula(axis(vCol, row), formula, PERCENT, false)
		}
	}

	// Print HORIZONTAL ANALYSIS title
	sheet.mergeCell(axis(2+wide+1, top+1), axis(2+wide+1, bottom))
	sheet.printCell(top+1, 1+wide+2, "ANÁLISE  HORIZONTAL", rotatedTextStyle)

	// CAGR (compound annual growth rate)
	// CAGR (t0, tn) = (V(tn)/V(t0))^(1/(tn-t0-1))-1
	vCol := (2 + wide + 2) + wide + 1
	sheet.printTitle(axis(vCol, top), "CAGR")
	for row := top + 1; row <= bottom; row++ {
		vt0 := axis(2, row)
		vtn := axis(2+wide, row)
		formula := fmt.Sprintf(`=IF(OR(%s="", %s="", %s=0, (%s*%s)<0), "", (%s/%s)^(1/%d)-1)`,
			vtn, vt0, vt0, vt0, vtn, vtn, vt0, wide)
		sheet.printFormula(axis(vCol, row), formula, PERCENT, false)
	}

	// ADJUST COLUMNS WIDTH
	sheet.autoWidth()

	// SECTOR REPORT
	if p.Reports["Sector"] {
		sheet2, err := e.newSheet("SETOR")
		if err == nil {
			sheet2.xlsx.SetSheetViewOptions(sheet2.name, 0,
				excelize.ShowGridLines(false),
				excelize.ZoomScale(80),
			)
			r.sectorReport(sheet2, p.Company)
		}
	}

	err = e.saveAndCloseExcel(p.Filename)
	if err == nil {
		fmt.Printf("[√] Dados salvos em %s\n", p.Filename)
	}

	return err
}

//
// sectorReport gets all the companies related to the 'company' and reports
// their financial summary
//
func (r report) sectorReport(sheet *Sheet, company string) (err error) {
	var interrupt bool

	// Handle Ctrl+C
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		fmt.Println("\n[ ] Processamento interrompido")
		interrupt = true
	}()

	// Companies from the same sector
	companies, secName, err := r.fromSector(company)
	if len(companies) <= 1 || err != nil {
		err = errors.Wrap(err, "erro ao ler arquivo de setores "+r.yamlFile)
		return
	}
	companies = append([]string{sectorAverage}, companies...)

	fmt.Println("[i] Criando relatório setorial (Ctrl+C para interromper)")
	var top, row, col int = 2, 0, 0
	var count int
	for _, co := range companies {
		row = top
		col++

		fmt.Printf("[ ] - %s", co)
		avg := false
		if co == sectorAverage {
			avg = true
			co = company
		}
		empty, err := r.companySummary(sheet, &row, &col, co, secName, count%3 == 0, avg)
		ok := "√"
		if err != nil || empty {
			ok = "x"
			col--
		} else {
			count++
			if count%3 == 0 {
				top = row + 2
				col = 0
			}
		}
		if interrupt {
			return nil
		}
		fmt.Printf("\r[%s\n", ok)
	}

	sheet.setColWidth(0, 2)

	return
}

//
// companySummary reports all companies from the same segment into the
// 'Setor' sheet.
//
func (r *report) companySummary(sheet *Sheet, row, col *int, _company, sectorName string, printDescr, sectorAvg bool) (empty bool, err error) {
	// if !sectorAvg && !r.isCompany(company) {
	// 	return true, nil
	// }

	cid, err := cid(r.db, _company)
	if err != nil {
		err = errors.Errorf("empresa '%s' não encontrada no banco de dados", _company)
		return
	}

	begin, end, err := timeRange(r.db)
	if err != nil {
		return
	}

	// Formats used in this report
	sTitle := newFormat(DEFAULT, RIGHT, true).newStyle(sheet.xlsx)
	fCompanyName := newFormat(DEFAULT, CENTER, true)
	fCompanyName.size(16)
	sCompanyName := fCompanyName.newStyle(sheet.xlsx)
	fSectorName := newFormat(DEFAULT, LEFT, false)
	fSectorName.size(14)
	sSectorName := fSectorName.newStyle(sheet.xlsx)
	//
	fDescr := newFormat(DEFAULT, RIGHT, false)
	fDescr.Border = []formatBorder{{Type: "left", Color: "333333", Style: 1}}
	sDescr := fDescr.newStyle(sheet.xlsx)
	fDescr.Border = []formatBorder{
		{Type: "top", Color: "333333", Style: 1},
		{Type: "left", Color: "333333", Style: 1},
	}
	sDescrTop := fDescr.newStyle(sheet.xlsx)
	fDescr.Border = []formatBorder{
		{Type: "top", Color: "333333", Style: 1},
	}
	sDescrBottom := fDescr.newStyle(sheet.xlsx)

	// Company name
	if printDescr {
		*col++
	}
	sheet.mergeCell(axis(*col, *row), axis(*col+end-begin+1, *row))
	if sectorAvg {
		sheet.printCell(*row-1, *col-1, sectorName, sSectorName)
		sheet.printCell(*row, *col, sectorAverage, sCompanyName)
	} else {
		sheet.printCell(*row, *col, _company, sCompanyName)
	}
	if printDescr {
		*col--
	}
	*row++

	// Save starting row
	rw := *row

	// Set width for the description col
	if printDescr {
		sheet.setColWidth(*col, 18)
		*col++
	}

	// Print values ONE YEAR PER COLUMN
	for y := begin; y <= end; y++ {
		var values map[uint32]float32
		var err error
		if sectorAvg {
			values, err = r.accountsAverage(_company, y)
			r.average = append(r.average, []float32{})
		} else {
			values, err = r.accountsValues(cid, y)
		}
		if err != nil {
			fmt.Printf(" -- %v", err)
			return false, err
		}

		// Skip last year if empty
		if y == end && sum(values) == 0 {
			end--
			break
		}

		*row = rw

		// Print year
		sheet.printCell(*row, *col, "["+strconv.Itoa(y)+"]", sTitle)
		*row++

		// Print financial metrics
		i := 0
		for _, metric := range metricsList(values) {
			if !r.groups[metric.group] {
				continue
			}
			if sectorAvg {
				r.average[y-begin] = append(r.average[y-begin], metric.val)
			}
			// Description
			if printDescr {
				stl := sDescr
				if i == 0 {
					stl = sDescrTop
				}
				sheet.printCell(*row, *col-1, metric.descr, stl)
			}
			// Values
			if metric.format != EMPTY {
				fVal := newFormat(metric.format, DEFAULT, false)
				fVal.Border = []formatBorder{
					{Type: "top", Color: "cccccc", Style: 1},
					{Type: "right", Color: "cccccc", Style: 1},
					{Type: "bottom", Color: "cccccc", Style: 1},
					{Type: "left", Color: "cccccc", Style: 1},
				}
				// Color the cell background according to its value compared with the average
				if len(r.average) > 0 && len(r.average[y-begin]) > 0 && len(r.average[y-begin]) >= i {
					f := formatFill{Type: "pattern", Pattern: 1}
					if metric.val > r.average[y-begin][i] {
						f.Color = []string{"c6efce"} // green
						fVal.Fill = f
					} else if metric.val < r.average[y-begin][i] {
						f.Color = []string{"ffc7ce"} // red
						fVal.Fill = f
					}
				}

				stl := fVal.newStyle(sheet.xlsx)
				sheet.printCell(*row, *col, metric.val, stl)
			}
			*row++
			i++
		}

		if printDescr {
			sheet.printCell(*row, *col-1, "", sDescrBottom)
		}

		printDescr = false
		*col++
	} // next year

	bottom := *row

	// CAGR (compound annual growth rate)
	// CAGR (t0, tn) = (V(tn)/V(t0))^(1/(tn-t0-1))-1
	wide := end - begin
	sheet.printTitle(axis(*col, rw), "CAGR")
	for r := rw + 1; r <= bottom; r++ {
		vt0 := axis(*col-wide-1, r)
		vtn := axis(*col-1, r)
		formula := fmt.Sprintf(`=IF(OR(%s="", %s="", %s=0, (%s*%s)<0), "", (%s/%s)^(1/%d)-1)`,
			vtn, vt0, vt0, vt0, vtn, vtn, vt0, wide)
		sheet.printFormula(axis(*col, r), formula, PERCENT, false)
	}
	*col++

	return
}

//
// metricsList returns the sequence to be printed after the financial statements
//
func metricsList(v map[uint32]float32) (metrics []metric) {
	dividaBruta := v[p.DividaCirc] + v[p.DividaNCirc]
	caixa := v[p.Caixa] + v[p.AplicFinanceiras]
	dividaLiquida := dividaBruta - caixa
	EBITDA := v[p.EBIT] - v[p.Deprec]
	proventos := v[p.Dividendos] + v[p.JurosCapProp]

	var roe float32
	if v[p.LucLiq] > 0 && v[p.EquityAvg] > 0 {
		roe = zeroIfNeg(safeDiv(v[p.LucLiq], v[p.EquityAvg]))
	}
	var cdg float32 = v[p.PassivoNCirc] + v[p.Equity] - v[p.AtivoNCirc]
	var ncg float32 = v[p.AtivoCirc] - v[p.Caixa] -
		(v[p.PassivoCirc] - v[p.Dividendos] - v[p.DividaCirc] - v[p.DividaNCirc])

	return []metric{
		{"Patrimônio Líquido", v[p.Equity], NUMBER, grpAccts},
		{"", 0, EMPTY, grpAccts},

		{"Receita Líquida", v[p.Vendas], NUMBER, grpAccts},
		{"EBITDA", EBITDA, NUMBER, grpAccts},
		{"EBIT", v[p.EBIT], NUMBER, grpAccts},
		{"Resultado Financeiro", v[p.ResulFinanc], NUMBER, grpAccts},
		{"Operações Descontinuadas", v[p.ResulOpDescont], NUMBER, grpAccts},
		{"Lucro Líquido", v[p.LucLiq], NUMBER, grpAccts},
		{"", 0, EMPTY, grpAccts},

		{"LPA", safeDiv(v[p.LucLiq]*v[p.Escala], v[p.Shares]), INDEX, grpAccts},
		{"", 0, EMPTY, grpAccts},

		{"Marg. EBITDA", zeroIfNeg(safeDiv(EBITDA, v[p.Vendas])), PERCENT, grpAccts},
		{"Marg. EBIT", zeroIfNeg(safeDiv(v[p.EBIT], v[p.Vendas])), PERCENT, grpAccts},
		{"Marg. Líq.", zeroIfNeg(safeDiv(v[p.LucLiq], v[p.Vendas])), PERCENT, grpAccts},
		{"ROE", roe, PERCENT, grpAccts},
		{"", 0, EMPTY, grpAccts},

		{"Caixa", caixa, NUMBER, grpAccts},
		{"Dívida Bruta", dividaBruta, NUMBER, grpAccts},
		{"Dívida Líq.", dividaLiquida, NUMBER, grpAccts},
		{"Dív. Bru./PL", zeroIfNeg(safeDiv(dividaBruta, v[p.Equity])), PERCENT, grpAccts},
		{"Dív.Líq./EBITDA", zeroIfNeg(safeDiv(dividaLiquida, EBITDA)), INDEX, grpAccts},
		{"", 0, EMPTY, grpAccts},

		{"FCO", v[p.FCO], NUMBER, grpAccts},
		{"FCI", v[p.FCI], NUMBER, grpAccts},
		{"FCF", v[p.FCF], NUMBER, grpAccts},
		{"FCT", v[p.FCO] + v[p.FCI] + v[p.FCF], NUMBER, grpAccts},
		{"FCL (FCO+FCI)", v[p.FCO] + v[p.FCI], NUMBER, grpAccts},
		{"", 0, EMPTY, grpAccts},

		{"Proventos", proventos, NUMBER, grpAccts},
		{"Payout", zeroIfNeg(safeDiv(proventos, v[p.LucLiq])), PERCENT, grpAccts},
		{"", 0, EMPTY, grpAccts},

		{"Total de Ações", v[p.Shares], GENERAL, grpShares},
		{"Free Float", v[p.FreeFloat], PERCENT, grpShares},
		{"", 0, EMPTY, grpShares},

		{"Liquidez Corrente", safeDiv(v[p.AtivoCirc], v[p.PassivoCirc]), INDEX, grpExtra},
		{"Liquidez Seco", safeDiv(v[p.AtivoCirc]-v[p.Estoque], v[p.PassivoCirc]), INDEX, grpExtra},
		{"Giro dos Ativos", safeDiv(v[p.Vendas], v[p.AtivoTotal]), INDEX, grpExtra},
		{"", 0, EMPTY, grpExtra},
		{"Giro de Estoque (dias)", safeDiv(v[p.EstoqueMedio], -v[p.CustoVendas]/360), INDEX, grpExtra},
		{"Prazo Médio de Recebimento (dias)", safeDiv(v[p.ContasARecebCirc]+v[p.ContasARecebNCirc], v[p.Vendas]/360), INDEX, grpExtra},
		{"", 0, EMPTY, grpExtra},
		{"Poder de Ganho Básico (PGB)", safeDiv(EBITDA, v[p.AtivoTotal]), PERCENT, grpExtra},
		{"ROA", safeDiv(v[p.LucLiq], v[p.AtivoTotal]), PERCENT, grpExtra},
		{"ROE", roe, PERCENT, grpExtra},
		{"", 0, EMPTY, grpExtra},

		{"-- Modelo Fleuriet --", 0, EMPTY, grpFleuriet},
		{"Capital de Giro (CDG)", cdg, NUMBER, grpFleuriet},
		{"Necessidade de Capital de Giro (NCG)", ncg, NUMBER, grpFleuriet},
		{"Saldo de Tesouraria (T)", cdg - ncg, NUMBER, grpFleuriet},
	}
}

func zeroIfNeg(n float32) float32 {
	if n < 0 {
		return 0
	}
	return n
}

func safeDiv(n, d float32) float32 {
	if d == 0 {
		return 0
	}
	return n / d
}

//
// ident returns the number of spaces according to the code level, e.g.:
// "1.1 ABC"   => "  " (2 spaces)
// "1.1.1 ABC" => "    " (4 spaces)
// For items equal or above 3, only returns spaces after 2nd level:
// "3.01 ABC"    => ""
// "3.01.01 ABC" => "  "
//
func ident(str string) (spaces string, baseItem bool) {
	num := strings.SplitN(str, ".", 2)[0]
	c := strings.Count(str, ".")
	if num != "1" && num != "2" && c > 0 {
		c--
	}
	if c > 0 {
		spaces = strings.Repeat("  ", c)
	}

	if num == "1" || num == "2" {
		baseItem = c <= 1
	} else {
		baseItem = c == 0
	}

	return
}

// printCodesAndDescription prints 'accounts' codes and descriptions on
// columns 'col' and 'col+1' (A <= col <= Z), starting on row 2.
// Adjust space related to the group, e.g.:
//  3.02 ABC <= print in bold if base item and stores the row position in baseItems[]
//    3.02.01 ABC
//
// Returns:
//  - []bool indicates if a row is a base item,
//  - the row of the last statement,
//  - the row of the last metric item.
func (r report) printCodesAndDescriptions(sheet *Sheet, accounts []accItems, col rune, row int) ([]bool, int, int) {
	baseItems := make([]bool, len(accounts)+row)
	for _, it := range accounts {
		var sp string
		sp, baseItems[row] = ident(it.cdConta)
		cell := string(col) + strconv.Itoa(row)
		sheet.print(cell, &[]string{sp + it.cdConta, sp + it.dsConta}, LEFT, baseItems[row])
		row++
	}
	lastStatementsRow := row - 1
	row += 2
	col++
	// Metrics descriptions
	for _, metric := range metricsList(nil) {
		if !r.groups[metric.group] {
			continue
		}
		if metric.descr != "" {
			cell := string(col) + strconv.Itoa(row)
			sheet.print(cell, &[]string{metric.descr}, RIGHT, false)
		}
		row++
	}
	lastMetricsRow := row - 1

	return baseItems, lastStatementsRow, lastMetricsRow
}

// sum returns a float32 with the sum of all values from a map
func sum(values map[uint32]float32) float32 {
	var sum float32
	for _, v := range values {
		sum += v
	}
	return sum
}
