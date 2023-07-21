package main

import (
	"context"
	"fmt"
	"log"
	"os"
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

var percent float64
var content string

type model struct {
	viewport viewport.Model
	progress progress.Model
	loading  bool
}

func newModel() (*model, error) {
	prog := progress.New(progress.WithScaledGradient("#FF7CCB", "#FDFF8C"))

	return &model{
		progress: prog,
		loading:  true,
	}, nil
}

func (m model) Init() tea.Cmd {
	return tickCmd()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit

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

		if percent > 1.0 {
			percent = 1.0
			m.loading = false

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
				panic(err)
			}

			str, err := renderer.Render(content)
			if err != nil {
				panic(err)
			}

			vp.SetContent(str)

			m.viewport = vp

			return m, nil
		}
		return m, tickCmd()
	default:
		return m, tea.ClearScreen

	}

}

const (
	padding  = 2
	maxWidth = 80
)

func (m model) View() string {
	if m.loading {
		pad := strings.Repeat(" ", padding)
		return "\n" +
			pad + m.progress.ViewAs(percent) + "\n\n" +
			pad + helpStyle("Press any key to quit")
	}

	return m.viewport.View() + m.helpView()
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

func main() {

	model, err := newModel()
	if err != nil {
		fmt.Println("Could not initialize Bubble Tea model:", err)
		os.Exit(1)
	}

	go func() {
		metadata, err := spotifyclient.GetCurrentTrack()
		if err != nil {
			fmt.Println("Seems that you don't have the spotify app desktop installed  or is not open :(")
			log.Fatalf("failed getting metadata, err: %s", err.Error())
		}

		fmt.Println(metadata)

		artistName := metadata.ArtistName[0]
		trackName := metadata.TrackName

		albumName := strings.ReplaceAll(strings.ToLower(metadata.AlbumName), "deluxe", "")
		albumName = strings.ReplaceAll(albumName, "expanded edition - remastered", "")
		albumName = strings.ReplaceAll(strings.ToLower(albumName), strings.ToLower("Bonus Tracks Edition"), "")

		type search struct {
			prompt string
			title  string
		}

		searches := []search{
			{
				prompt: fmt.Sprintf("Give me an album review (limit 500 characters) of %s %s", artistName, albumName),
				title:  "## Album Review",
			},
			{
				prompt: fmt.Sprintf("Give me album information (limit 500 characters) of %s %s", artistName, albumName),
				title:  "## Album Info",
			},
			{
				prompt: fmt.Sprintf("Give me album tracklist of %s %s", artistName, albumName),
				title:  "## Album Tracklist",
			},
			{
				prompt: fmt.Sprintf("Give me album credits of %s %s", artistName, albumName),
				title:  "## Album Credits",
			},
			{
				prompt: fmt.Sprintf("Give me song info (limit 500 characters) of %s %s", artistName, trackName),
				title:  "## Song Info",
			},
		}

		ch := make(chan string, len(searches))
		var wg sync.WaitGroup

		for _, search := range searches {
			wg.Add(1)
			go DoOpenAIRequest(search.title, search.prompt, ch, &wg, &percent)
		}

		wg.Wait()
		close(ch)

		for result := range ch {
			content += result
		}

	}()

	if _, err := tea.NewProgram(model).Run(); err != nil {
		fmt.Println("Bummer, there's been an error:", err)
		os.Exit(1)
	}

}

func DoOpenAIRequest(title string, query string, result chan<- string, wg *sync.WaitGroup, percent *float64) {
	defer wg.Done()

	client := openai.NewClient(os.Getenv("AUTH_TOKEN_OPEN_AI"))
	resp, err := client.CreateChatCompletion(
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
		fmt.Printf("ChatCompletion error: %v\n", err)
		return
	}

	content := title + "\n"
	content += resp.Choices[0].Message.Content + "\n"

	*percent += 0.25

	result <- content

}
