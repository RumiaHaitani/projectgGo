package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// Result хранит все данные по одному URL
type Result struct {
	URL        string
	StatusCode int
	OK         bool
	TTFBMs     int64
	SizeBytes  int64
	Contains   *bool
	Error      string
}

// readURLs читает URL из файла и возвращает их список
func readURLs(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var urls []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		url := strings.TrimSpace(scanner.Text())
		if url != "" {
			urls = append(urls, url)
		}
	}
	return urls, scanner.Err()
}

// checkURL проверяет один URL и возвращает заполненную структуру Result
func checkURL(url string, client *http.Client, contains string) Result {
	res := Result{URL: url}
	start := time.Now()

	// Выполняем запрос
	resp, err := client.Get(url)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()

	// TTFB – время до получения заголовков
	res.TTFBMs = time.Since(start).Milliseconds()
	res.StatusCode = resp.StatusCode
	res.OK = resp.StatusCode >= 200 && resp.StatusCode < 300

	// Читаем тело для подсчёта байт и поиска подстроки
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.SizeBytes = int64(len(body))

	if contains != "" {
		found := strings.Contains(string(body), contains)
		res.Contains = &found
	}
	return res
}

func main() {
	// Флаги командной строки (стандартные)
	file := flag.String("file", "urls.txt", "файл со списком URL")
	workers := flag.Int("workers", 5, "количество воркеров")
	contains := flag.String("contains", "", "подстрока для поиска в теле ответа")
	timeout := flag.Duration("timeout", 10*time.Second, "таймаут HTTP-запроса")

	// Ручной разбор --json (с опциональным значением)
	var jsonOutput string
	args := os.Args[1:]
	newArgs := []string{}
	skip := false
	for i, arg := range args {
		if skip {
			skip = false
			continue
		}
		if arg == "--json" {
			// Смотрим следующий аргумент: если он не флаг, это имя файла
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				jsonOutput = args[i+1]
				skip = true
			} else {
				jsonOutput = "results.json"
			}
			continue
		}
		if strings.HasPrefix(arg, "--json=") {
			jsonOutput = strings.TrimPrefix(arg, "--json=")
			continue
		}
		newArgs = append(newArgs, arg)
	}
	// Подменяем os.Args для flag.Parse
	os.Args = append([]string{os.Args[0]}, newArgs...)
	flag.Parse()

	urls, err := readURLs(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка чтения файла: %v\n", err)
		os.Exit(1)
	}
	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "Файл не содержит URL")
		os.Exit(1)
	}

	// Добавляем https:// если нет схемы
	for i, u := range urls {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			urls[i] = "https://" + u
		}
	}

	urlChan := make(chan string, len(urls))
	resultChan := make(chan Result, len(urls))

	var wg sync.WaitGroup
	client := &http.Client{Timeout: *timeout}

	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for url := range urlChan {
				resultChan <- checkURL(url, client, *contains)
			}
		}()
	}

	for _, u := range urls {
		urlChan <- u
	}
	close(urlChan)

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	var results []Result
	for res := range resultChan {
		results = append(results, res)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].URL < results[j].URL
	})

	printTable(results, *contains != "")

	if jsonOutput != "" {
		saveJSON(results, jsonOutput)
	}
}

// printTable выводит красиво отформатированную таблицу через tabwriter
func printTable(results []Result, showContains bool) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	// Заголовок
	fmt.Fprintln(w, "URL\tСтатус\tOK\tTTFB(ms)\tБайты\tСодержит\tОшибка")
	fmt.Fprintln(w, strings.Repeat("-", 80))

	// Строки
	for _, r := range results {
		status := fmt.Sprintf("%d", r.StatusCode)
		if r.StatusCode == 0 {
			status = "-"
		}
		ok := fmt.Sprintf("%t", r.OK)
		if r.Error != "" {
			ok = "false"
		}
		containsVal := "-"
		if r.Contains != nil {
			containsVal = fmt.Sprintf("%t", *r.Contains)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
			r.URL, status, ok, r.TTFBMs, r.SizeBytes, containsVal, r.Error)
	}
}

// saveJSON сохраняет результаты в формате JSON с отступами
func saveJSON(results []Result, filename string) {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка создания JSON: %v\n", err)
		return
	}
	err = os.WriteFile(filename, data, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка записи JSON-файла: %v\n", err)
		return
	}
	fmt.Printf("\nJSON сохранён в %s\n", filename)
}
