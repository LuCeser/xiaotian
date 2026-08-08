---
created: 2026-08-08T04:17:38.365Z
title: Research and fix iOS WebSocket audio playback issues for iPad devices
area: infra
files:
  - pkg/ws/server.go
  - pkg/tts/generator.go
  - test-audio-sequence-bug.html
  - test-interface-debug.html
---

## Problem

During actual testing on iOS/iPad devices, audio play functionality has issues with WebSocket streaming. A test page was previously added for convenience testing, but when tested on iPad Safari browser, the audio playback doesn't work as expected. The current WebSocket-based streaming TTS approach needs to be validated against iOS Safari's constraints and requirements.

## Solution

1. Research current WebSocket audio streaming implementation in the service layer
2. Read and analyze service documentation for TTS and WebSocket components  
3. Investigate iOS Safari-specific audio playback requirements and limitations
4. Test and confirm whether the current streaming approach can achieve the originally intended effects on iPad devices
5. Implement fixes if needed, potentially including iOS-specific workarounds or alternative approaches
6. Ensure the solution works within the project constraints (1C 1G VPS, <1.5s latency target, 50-100 concurrent sessions)

This research should determine if the full streaming TTS approach is viable for iOS Safari or if alternative strategies (like progressive download or iOS-specific buffering) are needed while maintaining the project's latency and concurrency requirements.