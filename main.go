package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Album описывает альбом с KHInsider.
type Album struct {
	ID      string
	URL     string
	Name    string
	Songs   []Song
	Formats []string
}

// Song описывает песню, принадлежащую саундтреку.
type Song struct {
	URL         string
	DownloadURL string
	Name        string
	Files       []File
}

// File описывает файл песни, который можно скачать.
type File struct {
	URL      string
	Filename string
}

// fetchPage загружает HTML-страницу и возвращает документ goquery.
func fetchPage(url string) (*goquery.Document, error) {
	res, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		return nil, fmt.Errorf("failed to fetch page: status code %d", res.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return nil, err
	}

	return doc, nil
}

func parseAlbum(url string) (*Album, error) {
	docAlb, err := fetchPage(url)
	if err != nil {
		return nil, err
	}

	album := &Album{
		URL:   url,
		Songs: []Song{},
	}

	var wg sync.WaitGroup
	songChannel := make(chan Song) // Канал для добавления треков
	errorChannel := make(chan error)

	// Находим все ссылки на треки
	docAlb.Find("table#songlist tr").Each(func(i int, s *goquery.Selection) {
		link := s.Find("a")
		if songURL, exists := link.Attr("href"); exists {
			wg.Add(1)
			go func(songURL, songName string) {
				defer wg.Done()

				// Получаем ссылку на скачивание
				downloadURL, err := getDownloadLink(songURL)
				if err != nil {
					errorChannel <- fmt.Errorf("Failed to get download link for song %s: %v", songName, err)
					return
				}

				// Отправляем трек в канал
				songChannel <- Song{
					URL:         songURL,
					Name:        songName,
					DownloadURL: downloadURL,
				}
			}(fmt.Sprintf("https://downloads.khinsider.com%s", songURL), link.Text())
		}
	})

	// Горутин для закрытия каналов
	go func() {
		wg.Wait()
		close(songChannel)
		close(errorChannel)
	}()

	// Чтение треков и ошибок из каналов
	for song := range songChannel {
		album.Songs = append(album.Songs, song)
	}
	for err := range errorChannel {
		log.Printf("Error fetching song: %v\n", err)
	}

	return album, nil
}

// getDownloadLink извлекает прямую ссылку на файл для скачивания со страницы песни.
func getDownloadLink(songPageURL string) (string, error) {
	res, err := http.Get(songPageURL)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		return "", fmt.Errorf("failed to load song page: %d", res.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return "", err
	}

	// Ищем ссылку на скачивание
	downloadLink, exists := doc.Find("a[href*='/soundtracks/']").Attr("href")
	if !exists {
		return "", fmt.Errorf("download link not found on song page")
	}

	return downloadLink, nil
}

// downloadFile загружает файл по URL и сохраняет его на диск.
func downloadFile(url string, path string) error {
	res, err := http.Get(url)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		return fmt.Errorf("failed to download file: status code %d", res.StatusCode)
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, res.Body)
	return err
}
							
// Download загружает все файлы песен альбома параллельно.
func (a *Album) Download(path string, numWorkers int) error {
	// Проверяем, существует ли папка, и создаём её, если её нет
	if err := os.MkdirAll(path, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	// Создаём канал для задач загрузки
	downloadJobs := make(chan Song)
	var wg sync.WaitGroup

	// Запуск воркеров для выполнения загрузки
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for song := range downloadJobs {
				// Очищаем и создаём имя файла
				filename := sanitizeFileName(fmt.Sprintf("%s.mp3", song.Name))
				filePath := filepath.Join(path, filename)

				fmt.Printf("Downloading: %s\n", song.DownloadURL)

				// Пытаемся скачать файл по прямой ссылке
				err := downloadFile(song.DownloadURL, filePath)
				if err != nil {
					log.Printf("Failed to download %s: %v\n", song.Name, err)
				} else {
					fmt.Printf("Downloaded: %s\n", song.Name)
				}

				// Задержка между загрузками (необязательно)
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}

	// Отправляем задания на загрузку в канал
	for _, song := range a.Songs {
		downloadJobs <- song
	}
	close(downloadJobs) // Закрываем канал, чтобы сигнализировать воркерам об окончании заданий

	// Ждём завершения всех воркеров
	wg.Wait()
	return nil
}

// sanitizeFileName удаляет или заменяет недопустимые символы в имени файла
func sanitizeFileName(name string) string {
	// Заменяем двоеточие на дефис
	name = strings.ReplaceAll(name, ":", "-")

	// Удаляем все недопустимые символы, такие как специальные символы
	re := regexp.MustCompile(`[<>:"/\\|?*]`)
	name = re.ReplaceAllString(name, "")

	// Убираем пробелы в начале и конце и заменяем их на подчеркивания
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, " ", "_")

	return name
}

func main() {
	soundtrackURL := "https://downloads.khinsider.com/game-soundtracks/album/minecraft-gamerip-2010" // измените на нужный URL

	soundtrack, err := parseAlbum(soundtrackURL)
	if err != nil {
		log.Fatalf("Failed to parse soundtrack: %v", err)
	}

	err = soundtrack.Download("./downloads", 5)
	if err != nil {
		log.Fatalf("Download failed: %v", err)
	}

	fmt.Println("Download complete.")
}
