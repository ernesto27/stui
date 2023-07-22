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
var percent float64

type model struct {
	viewport viewport.Model
	progress progress.Model
	loading  bool
	artist   string
	album    string
	track    string
	errMsg   string
	content  string
}

func main() {
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

	model, err := newModel(artistName, trackName, albumName)
	if err != nil {
		fmt.Println("Could not initialize Bubble Tea model:", err)
		os.Exit(1)
	}

	if artistName == "" {
		fmt.Println("Seems that you are listining to a podcast or something else...")
		os.Exit(1)
	}

	openaiClient = openai.NewClient(os.Getenv("OPENAI_TOKEN"))

	var mu sync.Mutex
	go model.getInfo(&mu, &percent)

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
		artist:   artist,
		track:    track,
		album:    album,
	}, nil
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
			percent = 0.0
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
		fmt.Println(time.Now())
		tickMsg := time.Time(msg)
		if tickMsg.Second()%2 == 0 {
			percent += 0.01
		}

		if percent >= 1.0 {
			percent = 1.0
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
			pad + m.progress.ViewAs(percent) + "\n\n" +
			pad + helpStyle("Press ctrl-c to quit")
	}

	errMsg := ""
	if m.errMsg != "" {
		errMsg = styleWarning(m.errMsg) + "\n\n"
	}

	return title + errMsg + m.viewport.View() + m.helpView()
}

func (e model) helpView() string {
	return helpStyle("\n  ↑/↓: Navigate • q: Quit\n")
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m *model) DoOpenAIRequest(title string, query string, result chan<- string, wg *sync.WaitGroup, percent *float64, mu *sync.Mutex) {
	defer wg.Done()

	resp, err := openaiClient.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model: openai.GPT3Dot5Turbo,
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
		*percent += 1.0
		return
	}

	c := title + "\n"
	c += resp.Choices[0].Message.Content + "\n"

	mu.Lock()
	*percent += 0.25
	mu.Unlock()

	result <- c
}

func (m *model) getInfo(mu *sync.Mutex, percent *float64) {
	type search struct {
		prompt string
		title  string
	}

	searches := []search{
		{
			prompt: fmt.Sprintf("Give me album information (limit 500 characters) of %s %s", m.artist, m.album),
			title:  "## Album Info",
		},
		{
			prompt: fmt.Sprintf("Give me album tracklist of %s %s", m.artist, m.album),
			title:  "## Album Tracklist",
		},
		{
			prompt: fmt.Sprintf("Give me album credits of %s %s", m.artist, m.album),
			title:  "## Album Credits",
		},
		{
			prompt: fmt.Sprintf("Give me song info (limit 500 characters) of %s %s", m.artist, m.track),
			title:  "## Song Info",
		},
	}

	ch := make(chan string, len(searches))
	var wg sync.WaitGroup

	for _, search := range searches {
		wg.Add(1)
		go m.DoOpenAIRequest(search.title, search.prompt, ch, &wg, percent, mu)
	}

	wg.Wait()
	close(ch)

	for result := range ch {
		m.content += result
	}

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
	m.content += `
## Links 
` + youtubeURL

	googleImagesURL := fmt.Sprintf("\nhttps://www.google.com/search?q=%s+%s&tbm=isch", bandNameQuery, albumNameQuery)
	m.content += `
` + googleImagesURL

	wikipediaURL := fmt.Sprintf("\nhttps://www.google.com/search?q=wikipedia+%s+%s", bandNameQuery, albumNameQuery)
	m.content += `
` + wikipediaURL
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
