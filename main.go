package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/ernesto27/spotifyclient"
	"github.com/sashabaranov/go-openai"
)

var helpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render
var styleTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("#b8ffcb")).MarginTop(1).Bold(true).Render
var styleWarning = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff7cc8")).Render

const (
	padding  = 2
	maxWidth = 80
)

var openaiClient *openai.Client

type MusicInfo struct {
	artist string
	album  string
	track  string
}

type model struct {
	viewport viewport.Model
	progress progress.Model
	loading  bool
	MusicInfo
	errMsg  string
	content string
	percent float64
	mu      *sync.Mutex
}

func main() {
	musicInfo := getSpotifyTrackInfo()

	model, err := newModel(musicInfo.artist, musicInfo.track, musicInfo.album)
	if err != nil {
		fmt.Println("Could not initialize Bubble Tea model:", err)
		os.Exit(1)
	}

	if musicInfo.artist == "" {
		fmt.Println("Seems that you are listining to a podcast or something else...")
		os.Exit(1)
	}

	openaiClient = openai.NewClient(os.Getenv("OPENAI_TOKEN"))

	model.mu = &sync.Mutex{}
	go model.getInfo()

	if _, err := tea.NewProgram(model).Run(); err != nil {
		fmt.Println("Bummer, there's been an error:", err)
		os.Exit(1)
	}
}

func newModel(artist, track, album string) (*model, error) {
	prog := progress.New(progress.WithScaledGradient("#FF7CCB", "#FDFF8C"))

	return &model{
		progress: prog,
		loading:  true,
		MusicInfo: MusicInfo{
			artist: artist,
			album:  album,
			track:  track,
		},
	}, nil
}

func getSpotifyTrackInfo() MusicInfo {
	metadata, err := spotifyclient.GetCurrentTrack()
	if err != nil {
		fmt.Println("Seems that you don't have the spotify app desktop installed  or is not open :(")
		os.Exit(1)
	}

	artistName := metadata.ArtistName[0]
	trackName := metadata.TrackName
	albumName := strings.ReplaceAll(strings.ToLower(metadata.AlbumName), "deluxe", "")
	albumName = strings.ReplaceAll(albumName, "expanded edition - remastered", "")
	albumName = strings.ReplaceAll(strings.ToLower(albumName), strings.ToLower("Bonus Tracks Edition"), "")

	return MusicInfo{
		artist: artistName,
		album:  albumName,
		track:  trackName,
	}
}

func (m model) Init() tea.Cmd {
	return tickCmd()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+r":
			m.loading = true
			m.percent = 0.0
			m.content = ""

			musicInfo := getSpotifyTrackInfo()
			m.MusicInfo = musicInfo
			go m.getInfo()

			return m, tickCmd()

		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	case tea.WindowSizeMsg:
		m.progress.Width = msg.Width - padding*2 - 4
		if m.progress.Width > maxWidth {
			m.progress.Width = maxWidth
		}
		return m, nil

	case tickMsg:
		m.mu.Lock()
		m.percent += 0.01
		m.mu.Unlock()

		if m.percent >= 1.0 {
			m.loading = false

			vp, err := NewViewport(m.content)
			if err != nil {
				panic(err)
			}
			m.viewport = vp

			return m, nil
		}
		return m, tickCmd()
	default:
		return m, tea.ClearScreen
	}
}

func (m *model) View() string {
	title := styleTitle(fmt.Sprintf("  %c %s - %s - %s", '♪', m.artist, m.album, m.track)) + "\n\n"
	if m.loading {
		pad := strings.Repeat(" ", padding)
		return "  " + title +
			pad + m.progress.ViewAs(m.percent) + "\n\n" +
			pad + helpStyle("Press ctrl-c to quit")
	}

	errMsg := ""
	if m.errMsg != "" {
		errMsg = styleWarning(m.errMsg) + "\n\n"
	}

	return title + errMsg + m.viewport.View() + m.helpView()
}

func (e model) helpView() string {
	return helpStyle("\n  ↑/↓: Navigate • ctrl-r Refresh • q: Quit \n")
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m *model) DoOpenAIRequest(title string, query string, wg *sync.WaitGroup) {
	defer wg.Done()

	resp, err := openaiClient.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:       openai.GPT3Dot5Turbo,
			Temperature: 0,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleUser,
					Content: query,
				},
			},
		},
	)

	if err != nil {
		m.errMsg = "  openai api: " + err.Error()
		m.percent += 1.0
		return
	}

	c := title + "\n"
	c += resp.Choices[0].Message.Content + "\n"

	m.mu.Lock()
	m.percent += 0.33
	m.content += c
	m.mu.Unlock()
}

func (m *model) getInfo() {
	type search struct {
		prompt string
		title  string
	}

	searches := []search{
		{
			prompt: fmt.Sprintf("Give me album info, tracklist and credits of %s %s", m.artist, m.album),
			title:  "## Album info and credits",
		},
		{
			prompt: fmt.Sprintf("Give me album review of %s %s", m.artist, m.album),
			title:  "## Album review",
		},
		{
			prompt: fmt.Sprintf("Give me song info (limit 500 characters) of %s %s", m.artist, m.track),
			title:  "## Song info",
		},
	}

	var wg sync.WaitGroup

	for _, search := range searches {
		wg.Add(1)
		go m.DoOpenAIRequest(search.title, search.prompt, &wg)
	}

	wg.Wait()

	bandNameQuery := strings.ReplaceAll(m.artist, " ", "+")
	songNameQuery := strings.ReplaceAll(m.track, " ", "+")
	albumNameQuery := strings.ReplaceAll(m.album, " ", "+")

	reg, err := regexp.Compile("[^a-zA-Z0-9]+")
	if err != nil {
		log.Fatal(err)
	}

	bandNameQuery = reg.ReplaceAllString(bandNameQuery, "+")
	albumNameQuery = reg.ReplaceAllString(albumNameQuery, "+")
	songNameQuery = reg.ReplaceAllString(songNameQuery, "+")

	youtubeURL := fmt.Sprintf("https://www.youtube.com/results?search_query=%s+%s", bandNameQuery, songNameQuery)
	googleImagesURL := fmt.Sprintf("\nhttps://www.google.com/search?q=%s+%s&tbm=isch", bandNameQuery, albumNameQuery)
	wikipediaURL := fmt.Sprintf("\nhttps://www.google.com/search?q=wikipedia+%s+%s", bandNameQuery, albumNameQuery)

	m.mu.Lock()
	m.content += `
## Links 
` + youtubeURL + "\n" + googleImagesURL + "\n" + wikipediaURL
	m.mu.Unlock()

}

func NewViewport(content string) (viewport.Model, error) {
	const width = 120
	vp := viewport.New(width, 40)
	vp.Style = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		PaddingRight(2)

	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return viewport.Model{}, err
	}

	str, err := renderer.Render(content)
	if err != nil {
		return viewport.Model{}, err
	}

	vp.SetContent(str)
	return vp, nil
}
