# Mimo Service Integration

Package `mimo` provides comprehensive integration with Xiaomi Mimo speech services (ASR & TTS) for the Home Tutor Agent. This package implements OpenAI-compatible API clients with authentication, error handling, and streaming support optimized for the project's 1C1G VPS constraints.

## Features

- **TTS (Text-to-Speech)**: Generate natural speech from text with multiple voice options
- **ASR (Automatic Speech Recognition)**: Transcribe audio to text with Chinese language support
- **Streaming Support**: Real-time audio streaming for low-latency applications
- **Error Handling**: Comprehensive error classification and retry logic
- **Rate Limiting**: Built-in retry mechanisms for transient failures
- **Resource Efficient**: Optimized for 1C1G VPS constraints (150MB Go service limit)

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "os"
    
    "github.com/xiaotian/home-tutor-agent/pkg/mimo"
    "github.com/xiaotian/home-tutor-agent/pkg/infra"
)

func main() {
    // Create client
    config := mimo.ClientConfig{
        APIKey: os.Getenv("MIMO_API_KEY"),
        Timeout: 30 * time.Second,
    }
    
    logger := infra.NewNoOpLogger()
    client, err := mimo.NewClient(config, logger)
    if err != nil {
        panic(err)
    }
    defer client.Close()
    
    // Generate speech
    ctx := context.Background()
    audio, err := client.Synthesize(ctx, "Hello, students!",
        mimo.WithVoice(mimo.VoiceBingTang),
        mimo.StyleEncouraging,
    )
    if err != nil {
        panic(err)
    }
    
    fmt.Printf("Generated %d bytes of audio\n", len(audio.Samples))
}
```

## Configuration

### Environment Variables

- `MIMO_API_KEY`: API authentication token (required)
- `MIMO_BASE_URL`: API endpoint (optional, defaults to production)

### Client Configuration

```go
config := mimo.ClientConfig{
    APIKey:     "your-api-key",
    BaseURL:    "https://api.xiaomimimo.com/v1",
    Timeout:    30 * time.Second,
    MaxRetries: 3,
    RetryDelay: 1 * time.Second,
}
```

## TTS Usage

### Basic Speech Generation

```go
audio, err := client.Synthesize(ctx, "你好，世界！",
    mimo.WithVoice(mimo.VoiceBingTang),
    mimo.WithAudioFormat(mimo.FormatPCM16),
    mimo.WithSampleRate(mimo.SampleRate16kHz),
)
```

### Using Presets

```go
// Dictation teacher voice
audio, err := client.Synthesize(ctx, "Spell the word: apple",
    mimo.PresetDictationTeacher...,
)

// Encouraging feedback
audio, err := client.Synthesize(ctx, "Great job! Keep practicing.",
    mimo.PresetEncouragingFeedback...,
)
```

### Streaming TTS

```go
callback := func(chunk mimo.StreamChunk) error {
    fmt.Printf("Received chunk: %d bytes\n", len(chunk.AudioData))
    return nil
}

result, err := client.SynthesizeStream(ctx, "This is a streaming test.",
    callback,
    mimo.WithVoice(mimo.VoiceMoLi),
)
```

## ASR Usage

### Basic Transcription

```go
audioData := []byte{/* audio samples */}
result, err := client.Transcribe(ctx, audioData, mimo.FormatPCM16,
    mimo.WithChildrenVoice(),
    mimo.WithPunctuation(),
)

fmt.Printf("Transcription: %s\n", result.Text)
fmt.Printf("Confidence: %.2f\n", result.Confidence)
```

### Using Presets

```go
// Children's dictation
result, err := client.Transcribe(ctx, audioData, mimo.FormatPCM16,
    mimo.PresetChildrenDictation...,
)

// Standard recognition
result, err := client.Transcribe(ctx, audioData, mimo.FormatPCM16,
    mimo.PresetStandardRecognition...,
)
```

## Voice Options

### Chinese Voices
- `mimo.VoiceBingTang` (冰糖): Gentle female voice, suitable for children
- `mimo.VoiceMoLi` (茉莉): Soft female voice, clear articulation  
- `mimo.VoiceSuDa` (苏打): Bright male voice, energetic
- `mimo.VoiceBaiHua` (白桦): Calm male voice, steady tone

### English Voices
- `mimo.VoiceMia`: English female voice, articulate
- `mimo.VoiceChloe`: English female voice, friendly
- `mimo.VoiceMilo`: English male voice, encouraging
- `mimo.VoiceDean`: English male voice, professional

## Style Presets

- `mimo.StyleEncouraging`: Gentle, encouraging tone for young learners
- `mimo.StyleTeacher`: Clear, instructional tone for classroom teaching
- `mimo.StyleStoryteller`: Engaging, animated tone for storytelling
- `mimo.StyleFeedback`: Supportive, corrective tone for error feedback

## Error Handling

The package provides comprehensive error handling:

```go
audio, err := client.Synthesize(ctx, "test")
if err != nil {
    switch {
    case errors.Is(err, mimo.ErrAuthentication):
        // Handle authentication failure
    case errors.Is(err, mimo.ErrRateLimit):
        // Handle rate limiting
    case errors.Is(err, mimo.ErrServiceUnavailable):
        // Handle service outage
    case mimo.IsRetryable(err):
        // Retry transient errors
    default:
        // Handle other errors
    }
}
```

## Resource Constraints

This package is optimized for 1C1G VPS constraints:

- **Connection Pooling**: Limited concurrent connections (max 20 per host)
- **Memory Management**: Efficient audio data handling
- **Timeout Control**: Configurable timeouts prevent resource exhaustion
- **Retry Limits**: Bounded retry attempts avoid infinite loops

## Testing

### Unit Tests

Run unit tests (no API key required):

```bash
go test ./pkg/mimo/... -v
```

### Integration Tests

Run integration tests with real API:

```bash
export MIMO_API_KEY="your-api-key"
go test ./pkg/mimo/... -v -run TestIntegration
```

### Test Coverage

```bash
go test ./pkg/mimo/... -cover
```

## Architecture

```
┌─────────────────────────────────────┐
│         Home Tutor Agent            │
│                                     │
│  ┌──────────────────────────────┐  │
│  │      Mimo Client             │  │
│  │  • Authentication            │  │
│  │  • Error Handling            │  │
│  │  • Retry Logic               │  │
│  │  • Streaming Support         │  │
│  └──────────────────────────────┘  │
│           ↓  ↑                     │
│  ┌──────────────────────────────┐  │
│  │    Xiaomi Mimo API           │  │
│  │  • TTS (mimo-v2.5-tts)       │  │
│  │  • ASR (mimo-v2.5-asr)       │  │
│  └──────────────────────────────┘  │
└─────────────────────────────────────┘
```

## Performance Considerations

- **Latency**: Target <1.5s for dictation responses
- **Concurrent Sessions**: Supports 50-100 concurrent sessions
- **Memory Usage**: Respects 150MB Go service limit
- **Connection Pooling**: Reuses connections for efficiency

## Integration with Existing Pipeline

The Mimo client integrates with the existing TTS pipeline:

1. **Session Start**: Create Mimo client with session-specific configuration
2. **TTS Generation**: Use `Synthesize()` for teacher speech generation
3. **Student Input**: Use `Transcribe()` for speech recognition
4. **Feedback**: Generate corrective feedback audio
5. **Session End**: Close client and release resources

## Troubleshooting

### Common Issues

1. **Authentication Errors**: Verify API key is correct and not expired
2. **Rate Limiting**: Implement backoff strategies for high-volume usage
3. **Network Issues**: Check connectivity and increase timeout values
4. **Memory Issues**: Monitor memory usage and adjust buffer sizes

### Debug Logging

Enable debug logging for troubleshooting:

```go
logger := infra.NewLogger(infra.LogConfig{
    Level: "debug",
})
```

## Future Enhancements

- Voice cloning support
- Pronunciation scoring integration
- Advanced dialect detection
- Multi-student session handling
- Real-time pronunciation feedback

## References

- [Xiaomi Mimo Documentation](https://mimo.mi.com/docs/)
- [Xiaomi Mimo Studio](https://aistudio.xiaomimimo.com/)
- [OpenAI API Compatibility](https://platform.openai.com/docs/api-reference)

## License

This package is part of the Home Tutor Agent project.