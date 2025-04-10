// pulseinsight
// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: 2025 Akihiro Yamamoto <github.com/ak1211>
// 各行に時間(s), RS485/422バスA線電圧(V), B線電圧(V)が
// 記録されたCSVファイルを解析する
package main

import (
	_ "embed"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"
	"golang.org/x/image/colornames"
	"golang.org/x/image/font/opentype"
	"gonum.org/v1/gonum/mat"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/font"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"
)

// 埋め込みIPAexフォント
var (
	//go:embed assets/ipaexg00401/ipaexg.ttf
	fontDataIpaexGothic []byte
	fontIpaexGothic     font.Font
)

const (
	ColTime            = 0   // 入力CSVの列1番目:時間(s)
	ColWireA           = 1   // 入力CSVの列2番目:RS485/422バスA線電圧(V)
	ColWireB           = 2   // 入力CSVの列3番目:RS485/422バスB線電圧(V)
	Threshould float64 = 1.0 // 差動通信のしきい値(V)
)

// 行列を表示する関数
func matPrint(X mat.Matrix) {
	fmt.Printf("%v\n", mat.Formatted(X, mat.Prefix(""), mat.Excerpt(0)))
}

// 解析対象のCSVファイルを読み込んで、行列を返す
func loadCsv(filePath string) (*mat.Dense, error) {
	// CSVファイルを開く
	f, err := os.Open(filePath)
	if err != nil {
		slog.Error("Open", "err", err)
		return nil, err
	}
	defer f.Close()

	// CSVリーダーを作成
	reader := csv.NewReader(f)

	// ヘッダー行と名前が書かれた行を読み飛ばす
	var skipLines int
	for skipLines = 0; skipLines < 2; skipLines++ {
		if _, err := reader.Read(); err != nil {
			slog.Error("Read", "err", err)
			return nil, err
		}
	}

	// 残りの行を読み込む
	records, err := reader.ReadAll()
	if err != nil {
		slog.Error("ReadAll", "err", err)
		return nil, err
	}

	// データを格納するスライスを作成
	data := []float64{}
	rows := len(records)
	cols := len(records[0])

	// CSVデータをスライスに変換
	for r, record := range records {
		for c, value := range record {
			var floatValue float64
			if value == "" {
				slog.Warn("assigned to Zero", "row", skipLines+1+r, "column", 1+c)
				// 空カラムには0を割り当てる
				floatValue = 0.0
			} else {
				floatValue, err = strconv.ParseFloat(value, 64)
				if err != nil {
					slog.Error("ParseFloat", "err", err)
					return nil, err
				}
			}
			data = append(data, floatValue)
		}
	}

	// 行列を作成
	return mat.NewDense(rows, cols, data), nil
}

type UartBit struct {
	startTime float64
	endTime   float64
	state     string
	bit       int
}

func (b UartBit) toString() string {
	return fmt.Sprintf("%d (%s)", b.bit, b.state)
}

type UartCode struct {
	startTime float64
	endTime   float64
	octet     byte
}

func (c UartCode) toString() string {
	return fmt.Sprintf("(%08b)\n%d, 0x%02x, '%c'", c.octet, c.octet, c.octet, c.octet)
}

type ChartOption struct {
	titleText     string
	xLabelText    string
	yLabelText    string
	uartBitValues []UartBit
	uartCodes     []UartCode
}

// グラフを保存する
func saveChart(savefilepath string, graphWidth int, graphHeight int, option ChartOption, matrix mat.Matrix) error {
	p := plot.New()

	p.Title.Text = option.titleText
	p.X.Label.Text = option.xLabelText
	p.Y.Label.Text = option.yLabelText

	// 背景色
	p.BackgroundColor = colornames.Snow

	// 補助線
	//	p.Add(plotter.NewGrid())

	// 凡例の位置を右下に設定
	p.Legend.Top = false
	p.Legend.Left = false
	p.Legend.Padding = vg.Points(5)

	rows, cols := matrix.Dims()

	if cols < 3 {
		slog.Error("列数が不足")
		return nil
	}

	// A線電圧
	wireA := make(plotter.XYs, rows)
	for row := range wireA {
		wireA[row].X = matrix.At(row, ColTime)
		wireA[row].Y = matrix.At(row, ColWireA)
	}
	// 折れ線グラフを作成
	if line, points, err := plotter.NewLinePoints(wireA); err != nil {
		slog.Error("NewLine", "err", err)
	} else {
		points.Shape = draw.CrossGlyph{}
		line.Color = colornames.Darkmagenta
		p.Add(line, points)
		p.Legend.Add("A線", line) // 凡例
	}

	// B線電圧
	wireB := make(plotter.XYs, rows)
	for row := range wireA {
		wireB[row].X = matrix.At(row, ColTime)
		wireB[row].Y = matrix.At(row, ColWireB)
	}
	// 折れ線グラフを作成
	if line, points, err := plotter.NewLinePoints(wireB); err != nil {
		slog.Error("NewLine", "err", err)
	} else {
		points.Shape = draw.CrossGlyph{}
		line.Color = colornames.Darkcyan
		p.Add(line, points)
		p.Legend.Add("B線", line) // 凡例
	}

	// 各々ビットの値
	if len(option.uartBitValues) != 0 {
		labelPoints := make([]plotter.XY, len(option.uartBitValues))
		labelTexts := make([]string, len(option.uartBitValues))
		for i, v := range option.uartBitValues {
			labelPoints[i].X = v.startTime
			labelPoints[i].Y = 0
			labelTexts[i] = v.toString()
		}
		// データポイントにラベルを追加
		labels, err := plotter.NewLabels(plotter.XYLabels{
			XYs:    labelPoints,
			Labels: labelTexts,
		})
		if err != nil {
			slog.Error("NewLabels", "err", err)
			return err
		}
		// ラベルの回転を設定
		for i := range labels.TextStyle {
			labels.TextStyle[i].Rotation = -math.Pi / 2 // 右90度回転
		}
		// ラベルを追加する
		p.Add(labels)
	}

	//
	if len(option.uartCodes) != 0 {
		labelPoints := make([]plotter.XY, len(option.uartCodes))
		labelTexts := make([]string, len(option.uartCodes))
		for i, v := range option.uartCodes {
			labelPoints[i].X = v.startTime
			labelPoints[i].Y = -1
			labelTexts[i] = v.toString()
		}
		// データポイントにラベルを追加
		labels, err := plotter.NewLabels(plotter.XYLabels{
			XYs:    labelPoints,
			Labels: labelTexts,
		})
		if err != nil {
			slog.Error("NewLabels", "err", err)
			return err
		}
		// ラベル
		for i := range labels.TextStyle {
			labels.TextStyle[i].Font.Size = 22
			labels.TextStyle[i].Color = colornames.Darkgreen
		}
		// ラベルを追加する
		p.Add(labels)
	}

	// プロットを画像ファイルに保存
	if err := p.Save(vg.Points(float64(graphWidth)), vg.Points(float64(graphHeight)), savefilepath); err != nil {
		log.Fatalf("could not save plot: %v", err)
	}

	return nil
}

// 移動平均フィルタを掛ける
func applySmoothing(original mat.Matrix, windowSize int) (mat.Matrix, error) {
	rows, cols := original.Dims()

	if rows < windowSize {
		return nil, errors.New("データ数が不足している")
	}

	if cols != 3 {
		slog.Warn("期待している列数と違う")
	}

	var averageA float64
	var averageB float64
	for r := 0; r < windowSize; r++ {
		averageA += original.At(r, ColWireA)
		averageB += original.At(r, ColWireB)
	}
	averageA /= float64(windowSize)
	averageB /= float64(windowSize)

	// データを格納するスライスを作成
	filteredData := []float64{}

	// 移動平均
	for r := 0; r < rows-windowSize; r++ {
		time := original.At(r+windowSize, ColTime)
		filteredData = append(filteredData, time, averageA, averageB)
		// 最初の値を引く
		averageA -= original.At(r, ColWireA) / float64(windowSize)
		averageB -= original.At(r, ColWireB) / float64(windowSize)
		// 現在の値を足す
		averageA += original.At(r+windowSize, ColWireA) / float64(windowSize)
		averageB += original.At(r+windowSize, ColWireB) / float64(windowSize)
	}

	matrix := mat.NewDense(rows-windowSize, cols, filteredData)
	return matrix, nil
}

// 波形整形
func reshapeWaveform(original mat.Matrix, baudrate int) (mat.Matrix, error) {
	matrix := mat.DenseCopyOf(original)
	rows, _ := matrix.Dims()

	// スタートビット開始時間を検出する
	var startbitTime float64
	for r := 0; r < rows; r++ {
		if matrix.At(r, ColWireA)-matrix.At(r, ColWireB) < -Threshould {
			startbitTime = matrix.At(r, ColTime)
			break
		}
	}
	// 各々の時間をスタートビット開始時間との相対時間にする
	for r := 0; r < rows; r++ {
		t := matrix.At(r, ColTime)
		matrix.Set(r, ColTime, t-startbitTime)
	}

	// 周期T
	T := 1 / float64(baudrate)

	// データを格納するスライスを作成
	data := []float64{}

	for r := 0; r < rows; r++ {
		startTime := matrix.At(r, ColTime)
		// 差動伝送なのでA,B間電圧差が正(A線+,B線-)の時にMark、負(A線-,B線+)の時にSpace
		d := matrix.At(r, ColWireA) - matrix.At(r, ColWireB)
		if d > Threshould {
			// Mark
			data = append(data, startTime, 1, -1) // Mark開始時間
			var endTime float64 = startTime
			// 継続時間
			for ; endTime-startTime < T && r < rows; r++ {
				e := matrix.At(r, ColWireA) - matrix.At(r, ColWireB)
				if e > Threshould {
					endTime = matrix.At(r, ColTime)
				} else {
					break
				}
			}
			data = append(data, endTime, 1, -1) // Mark終了時間
		} else if d < -Threshould {
			// Space
			data = append(data, startTime, -1, 1) // Space開始時間
			var endTime float64 = startTime
			// 継続時間
			for ; endTime-startTime < T && r < rows; r++ {
				e := matrix.At(r, ColWireA) - matrix.At(r, ColWireB)
				if e < -Threshould {
					endTime = matrix.At(r, ColTime)
				} else {
					break
				}
			}
			data = append(data, endTime, -1, 1) // Space終了時間
		} else {
			// 閾値以下はノイズなので追加しない
		}
	}

	newMatrix := mat.NewDense(len(data)/3, 3, data)
	return newMatrix, nil
}

// 解析
func analyzePulses(reshaped mat.Matrix) ([]UartBit, []UartCode, error) {
	rows, cols := reshaped.Dims()

	if cols != 3 {
		slog.Warn("期待している列数と違う")
	}

	// 状態
	var state string = "IDLE"
	// コード
	var octet uint8

	// 状態移行
	shiftState := func(bit uint8) {
		bit &= 1
		switch state {
		case "IDLE":
			if bit == 0 { // バスアイドル状態からA線が下降したら開始
				state = "START"
			}
			octet = 0 // 初期化

		case "START":
			state = "Bit#0"
			octet = 0         // 初期化
			octet |= bit << 0 // Bit#0

		case "Bit#0":
			state = "Bit#1"
			octet |= bit << 1 // Bit#1

		case "Bit#1":
			state = "Bit#2"
			octet |= bit << 2 // Bit#2

		case "Bit#2":
			state = "Bit#3"
			octet |= bit << 3 // Bit#3

		case "Bit#3":
			state = "Bit#4"
			octet |= bit << 4 // Bit#4

		case "Bit#4":
			state = "Bit#5"
			octet |= bit << 5 // Bit#5

		case "Bit#5":
			state = "Bit#6"
			octet |= bit << 6 // Bit#6

		case "Bit#6":
			state = "Bit#7"
			octet |= bit << 7 // Bit#7

		case "Bit#7":
			if bit == 1 {
				state = "STOP" // パリティなしなのでここまで
			} else {
				state = "X"
			}

		case "STOP":
			if bit == 1 {
				state = "IDLE"
			} else {
				state = "START"
			}

		case "X":
			if bit == 1 {
				state = "STOP"
			} else {
				state = "START"
			}

		default:
			state = "X"
		}
	}
	// データを格納するスライスを作成
	signal := []UartBit{}
	codes := []UartCode{}
	var startOctetTime float64

	for r := 0; r < rows; r += 2 {
		startTime := reshaped.At(r, ColTime)
		startA := reshaped.At(r, ColWireA)
		startB := reshaped.At(r, ColWireB)
		diff := startA - startB
		endTime := reshaped.At(r+1, ColTime)
		endA := reshaped.At(r+1, ColWireA)
		endB := reshaped.At(r+1, ColWireB)
		if startA != endA || startB != endB {
			return nil, nil, errors.New("データ不一致")
		}
		if diff > Threshould {
			// Mark
			// Logical: 1
			shiftState(1)
			signal = append(signal, UartBit{startTime, endTime, state, 1})
		} else if diff < -Threshould {
			// Space
			// Logical: 0
			shiftState(0)
			signal = append(signal, UartBit{startTime, endTime, state, 0})
		} else {
			continue
		}
		if state == "START" {
			startOctetTime = startTime
		} else if state == "STOP" {
			codes = append(codes, UartCode{startOctetTime, endTime, octet})
		}
	}

	return signal, codes, nil
}

// CSVファイルを調べる
func insightTheCsvFile(csvfilepath string, baudrate int, graphWidth int, graphHeight int) error {
	fmt.Printf("input file \"%s\"\n", csvfilepath)

	// 解析対象の行列
	matrix, err := loadCsv(csvfilepath)
	if err != nil {
		slog.Error("loadCsv", "err", err)
		return err
	}

	// 入力ファイル拡張子
	ext := filepath.Ext(csvfilepath)

	// 入力ファイル拡張子を取り除く
	basename := strings.TrimSuffix(csvfilepath, ext)

	// グラフファイル
	chartfile := basename + "_" + ext[1:] + "_voltage.png"

	// グラフをファイルに保存
	var chartOption = ChartOption{
		titleText:     "A,B線電圧の時間変化",
		xLabelText:    "時間(s)",
		yLabelText:    "電圧(V)",
		uartBitValues: []UartBit{},
		uartCodes:     []UartCode{},
	}
	saveChart(chartfile, graphWidth, graphHeight, chartOption, matrix)

	// ローパスフィルタ適用
	filtered, err := applySmoothing(matrix, 8)
	if err != nil {
		slog.Error("applySmoothing", "err", err)
		return err
	}

	// フィルタ後グラフファイル
	filteredChartFile := basename + "_" + ext[1:] + "_filtered.png"

	// グラフをファイルに保存
	chartOption.titleText = "ローパスフィルタ適用後"
	saveChart(filteredChartFile, graphWidth, graphHeight, chartOption, filtered)

	// 波形整形
	reshaped, err := reshapeWaveform(matrix, baudrate)
	if err != nil {
		slog.Error("reshapeWaveform", "err", err)
		return err
	}

	// 波形整形後グラフファイル
	reshapedChartFile := basename + "_" + ext[1:] + "_reshaped.png"

	// グラフをファイルに保存
	chartOption.titleText = "波形整形後"
	chartOption.yLabelText = "[1,-1]正規化"
	saveChart(reshapedChartFile, graphWidth, graphHeight, chartOption, reshaped)

	// 解析
	uartBitValues, uartCodes, err := analyzePulses(reshaped)
	if err != nil {
		slog.Error("analyzePulses", "err", err)
		return err
	}

	// グラフファイル
	uartChartFile := basename + "_" + ext[1:] + "_uart.png"

	// グラフをファイルに保存
	chartOption.titleText = "UART通信"
	chartOption.yLabelText = "[1,-1]正規化"
	chartOption.uartBitValues = uartBitValues
	chartOption.uartCodes = uartCodes
	saveChart(uartChartFile, graphWidth, graphHeight, chartOption, reshaped)

	// 表示
	bytes := []byte{}
	for _, v := range uartCodes {
		bytes = append(bytes, v.octet)
	}
	if len(bytes) > 0 {
		stdoutDumper := hex.Dumper(os.Stdout)
		defer stdoutDumper.Close()
		binary.Write(stdoutDumper, binary.LittleEndian, bytes)
	}

	if false {
		matPrint(matrix)
	}

	return nil
}

func init() {
	// IPAexゴシックフォントを準備する
	ttf, err := opentype.Parse(fontDataIpaexGothic)
	if err != nil {
		panic(err)
	}

	fontIpaexGothic = font.Font{Typeface: "IPAexGothic"}
	font.DefaultCache.Add([]font.Face{
		{
			Font: fontIpaexGothic,
			Face: ttf,
		},
	})

	if !font.DefaultCache.Has(fontIpaexGothic) {
		panic(fmt.Errorf("typeface %s, font load error", fontIpaexGothic.Typeface))
	}

	// デフォルトフォントをIPAexゴシックにする
	plot.DefaultFont = fontIpaexGothic
	plotter.DefaultFont = fontIpaexGothic
}

func main() {
	var (
		baudrate    int
		graphWidth  int
		graphHeight int
	)

	app := &cli.App{
		Name:    "pulseinsight",
		Usage:   "RS485バスの測定値を解析する",
		Version: "1.0.0",
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:        "baudrate",
				Aliases:     []string{"baud"},
				Usage:       "ボーレート",
				Destination: &baudrate,
				Value:       9600,
			},
			&cli.IntFlag{
				Name:        "width",
				Aliases:     []string{"W", "Wpx"},
				Usage:       "グラフの横ピクセル",
				Destination: &graphWidth,
				Value:       640 * 16,
			},
			&cli.IntFlag{
				Name:        "height",
				Aliases:     []string{"H", "Hpx"},
				Usage:       "グラフの縦ピクセル",
				Destination: &graphHeight,
				Value:       640,
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "csv",
				Usage: "CSVファイルを解析する",
				Action: func(c *cli.Context) error {
					csvfile := c.Args().First()
					if len(csvfile) == 0 {
						return cli.Exit("ファイルが指定されていません", -1)
					}
					err := insightTheCsvFile(c.Args().First(), baudrate, graphWidth, graphHeight)
					if err != nil {
						slog.Error("insightTheCsvFile", "err", err)
						return err
					}
					return nil
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		slog.Error("app.Run", "err", err)
		return
	}
}
