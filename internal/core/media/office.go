package media

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/xuri/excelize/v2"
)

func extractDOCXText(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	for _, file := range r.File {
		if file.Name != "word/document.xml" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		return collectWordprocessingMLText(rc)
	}
	return "", fmt.Errorf("word/document.xml not found")
}

func collectWordprocessingMLText(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var b strings.Builder
	inText := false
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch typed := tok.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteString("\t")
			case "br", "cr":
				b.WriteString("\n")
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "t":
				inText = false
			case "p", "tr":
				b.WriteString("\n")
			case "tc":
				b.WriteString("\t")
			}
		case xml.CharData:
			if inText {
				b.Write([]byte(typed))
			}
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func extractPPTXText(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	type slideFile struct {
		number int
		file   *zip.File
	}
	slides := make([]slideFile, 0)
	for _, file := range r.File {
		if !strings.HasPrefix(file.Name, "ppt/slides/slide") || !strings.HasSuffix(file.Name, ".xml") {
			continue
		}
		number := trailingNumber(file.Name)
		slides = append(slides, slideFile{number: number, file: file})
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].number < slides[j].number })
	var sections []string
	for idx, slide := range slides {
		rc, err := slide.file.Open()
		if err != nil {
			return "", err
		}
		text, err := collectPresentationText(rc)
		rc.Close()
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		number := slide.number
		if number <= 0 {
			number = idx + 1
		}
		sections = append(sections, fmt.Sprintf("## Slide %d\n%s", number, text))
	}
	if len(sections) == 0 {
		return "", fmt.Errorf("no slide text found")
	}
	return strings.Join(sections, "\n\n"), nil
}

func collectPresentationText(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var lines []string
	var current strings.Builder
	inText := false
	flush := func(force bool) {
		line := strings.TrimSpace(current.String())
		if line != "" || force {
			if line != "" {
				lines = append(lines, line)
			}
		}
		current.Reset()
	}
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch typed := tok.(type) {
		case xml.StartElement:
			if typed.Name.Local == "t" {
				inText = true
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "t":
				inText = false
			case "p":
				flush(false)
			}
		case xml.CharData:
			if inText {
				current.Write([]byte(typed))
			}
		}
	}
	flush(false)
	return strings.Join(lines, "\n"), nil
}

func extractXLSXText(path string) (string, error) {
	file, err := excelize.OpenFile(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sheets := file.GetSheetList()
	sections := make([]string, 0, len(sheets))
	for _, sheet := range sheets {
		rows, err := file.GetRows(sheet)
		if err != nil {
			return "", err
		}
		lines := make([]string, 0, len(rows))
		for _, row := range rows {
			last := len(row) - 1
			for last >= 0 && strings.TrimSpace(row[last]) == "" {
				last--
			}
			if last < 0 {
				continue
			}
			lines = append(lines, strings.Join(row[:last+1], "\t"))
		}
		if len(lines) == 0 {
			continue
		}
		sections = append(sections, fmt.Sprintf("## Sheet: %s\n%s", sheet, strings.Join(lines, "\n")))
	}
	if len(sections) == 0 {
		return "", fmt.Errorf("no spreadsheet text found")
	}
	return strings.Join(sections, "\n\n"), nil
}
