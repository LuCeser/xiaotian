package mimo

import (
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// MimoConfig holds Mimo service configuration loaded from the main config
type MimoConfig struct {
	// APIKey is the authentication token for Mimo API (loaded from env var)
	APIKey string
	// BaseURL is the API endpoint (default: https://api.xiaomimimo.com/v1)
	BaseURL string
	// TimeoutSeconds is the HTTP request timeout in seconds (default: 30s)
	TimeoutSeconds int
	// MaxRetries is the number of retry attempts for transient failures (default: 3)
	MaxRetries int
	// RetryDelaySeconds is the initial delay between retries in seconds (default: 1s)
	RetryDelaySeconds int
	// EnableStreaming enables SSE streaming for TTS (default: true)
	EnableStreaming bool
}

// DefaultMimoConfig returns default Mimo configuration optimized for 1C1G VPS
func DefaultMimoConfig() MimoConfig {
	return MimoConfig{
		BaseURL:           "https://api.xiaomimimo.com/v1",
		TimeoutSeconds:    30,
		MaxRetries:         3,
		RetryDelaySeconds:  1,
		EnableStreaming:   true,
	}
}

// ClientConfig converts MimoConfig to ClientConfig
func (c MimoConfig) ToClientConfig() ClientConfig {
	timeout := c.TimeoutSeconds
	if timeout == 0 {
		timeout = 30
	}

	retries := c.MaxRetries
	if retries == 0 {
		retries = 3
	}

	delay := c.RetryDelaySeconds
	if delay == 0 {
		delay = 1
	}

	return ClientConfig{
		APIKey:         c.APIKey,
		BaseURL:        c.BaseURL,
		Timeout:        durationSeconds(timeout),
		MaxRetries:     retries,
		RetryDelay:     durationSeconds(delay),
		EnableStreaming: c.EnableStreaming,
	}
}

// durationSeconds converts seconds to time.Duration
func durationSeconds(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

// NewClientFromConfig creates a Mimo client from configuration
func NewClientFromConfig(config MimoConfig, logger infra.Logger) (Client, error) {
	if config.APIKey == "" {
		return nil, ErrAPIKeyRequired
	}

	clientConfig := config.ToClientConfig()
	return NewClient(clientConfig, logger)
}

// Voice presets optimized for education use
const (
	// VoiceBingTang is the "冰糖" voice - gentle female voice, suitable for children
	VoiceBingTang = "冰糖"

	// VoiceMoLi is the "茉莉" voice - soft female voice, clear articulation
	VoiceMoLi = "茉莉"

	// VoiceSuDa is the "苏打" voice - bright male voice, energetic
	VoiceSuDa = "苏打"

	// VoiceBaiHua is the "白桦" voice - calm male voice, steady tone
	VoiceBaiHua = "白桦"

	// VoiceMia is the "Mia" voice - English female voice, articulate
	VoiceMia = "Mia"

	// VoiceChloe is the "Chloe" voice - English female voice, friendly
	VoiceChloe = "Chloe"

	// VoiceMilo is the "Milo" voice - English male voice, encouraging
	VoiceMilo = "Milo"

	// VoiceDean is the "Dean" voice - English male voice, professional
	VoiceDean = "Dean"
)

// Style presets for educational contexts
var (
	// StyleEncouraging uses a gentle, encouraging tone suitable for young learners
	StyleEncouraging = WithStyleInstruction("Use a gentle, encouraging tone suitable for elementary students. Be supportive and patient.")

	// StyleTeacher uses a clear, instructional tone suitable for classroom teaching
	StyleTeacher = WithStyleInstruction("Use a clear, teacher-like tone suitable for classroom instruction. Speak distinctly and at a moderate pace.")

	// Storyteller uses an engaging, animated tone suitable for storytelling
	StyleStoryteller = WithStyleInstruction("Use an engaging, animated tone suitable for storytelling. Be expressive and bring the story to life.")

	// StyleFeedback uses a supportive, corrective tone suitable for error feedback
	StyleFeedback = WithStyleInstruction("Use a supportive, corrective tone when providing feedback. Be constructive and maintain student confidence.")
)

// Audio format constants
const (
	// FormatPCM16 is 16-bit PCM format (raw audio)
	FormatPCM16 = "pcm16"

	// FormatWAV is WAV format (with headers)
	FormatWAV = "wav"

	// FormatMP3 is MP3 format (compressed)
	FormatMP3 = "mp3"
)

// Common sample rates
const (
	// SampleRate16kHz is 16,000 Hz (default for speech recognition)
	SampleRate16kHz = 16000

	// SampleRate24kHz is 24,000 Hz (higher quality)
	SampleRate24kHz = 24000

	// SampleRate48kHz is 48,000 Hz (high quality)
	SampleRate48kHz = 48000
)

// Preset configurations for different use cases
var (
	// PresetDictationTeacher is optimized for dictation teacher voice
	PresetDictationTeacher = []TTSOption{
		WithVoice(VoiceMoLi),
		WithAudioFormat(FormatPCM16),
		WithSampleRate(SampleRate16kHz),
		StyleTeacher,
	}

	// PresetEncouragingFeedback is optimized for positive feedback
	PresetEncouragingFeedback = []TTSOption{
		WithVoice(VoiceBingTang),
		WithAudioFormat(FormatPCM16),
		WithSampleRate(SampleRate16kHz),
		StyleEncouraging,
	}

	// PresetStorytelling is optimized for reading stories
	PresetStorytelling = []TTSOption{
		WithVoice(VoiceBingTang),
		WithAudioFormat(FormatWAV),
		WithSampleRate(SampleRate24kHz),
		StyleStoryteller,
	}
)

// ASR preset configurations
var (
	// PresetChildrenDictation is optimized for children's dictation
	PresetChildrenDictation = []ASROption{
		WithChildrenVoice(),
		WithPunctuation(),
	}

	// PresetStandardRecognition is for general speech recognition
	PresetStandardRecognition = []ASROption{
		WithPunctuation(),
	}

	// PresetDialectSupport includes dialect detection
	PresetDialectSupport = []ASROption{
		WithChildrenVoice(),
		WithPunctuation(),
		WithDialectDetection(),
	}
)