/**
 * End-to-end test for multi-word dictation flow.
 * Verifies:
 * 1. session_ready is parsed correctly (no "connection error")
 * 2. Multiple words complete in sequence
 *
 * Run: node test-e2e-multi-word.js
 */

const WebSocket = require('ws');
const spawn = require('child_process').spawn;

class E2ETest {
  constructor() {
    this.serverProcess = null;
    this.ws = null;
    this.receivedFrames = [];
    this.errors = [];
    this.timeoutMs = 15000; // 15 seconds timeout
  }

  async startServer() {
    console.log('[TEST] Starting server...');
    this.serverProcess = spawn('./service.exe', [], {
      cwd: process.cwd(),
      stdio: ['ignore', 'pipe', 'pipe']
    });

    // Log all output for debugging
    this.serverProcess.stdout.on('data', (data) => {
      console.log('[SERVER]', data.toString().trim());
    });

    this.serverProcess.stderr.on('data', (data) => {
      console.log('[SERVER STDERR]', data.toString().trim());
    });

    this.serverProcess.on('error', (err) => {
      console.error('[TEST] Failed to start server:', err);
      throw err;
    });

    // Wait for server to start and show "listening" message
    await new Promise(resolve => setTimeout(resolve, 3000));
    console.log('[TEST] Server started (PID:', this.serverProcess.pid, ')');
  }

  async stopServer() {
    if (this.serverProcess) {
      console.log('[TEST] Stopping server...');
      this.serverProcess.kill('SIGTERM');
      await new Promise(resolve => setTimeout(resolve, 1000));
      console.log('[TEST] Server stopped');
    }
  }

  connect() {
    return new Promise((resolve, reject) => {
      console.log('[TEST] Connecting to WebSocket...');
      this.ws = new WebSocket('ws://localhost:8080/ws');
      this.ws.binaryType = 'arraybuffer';

      this.ws.on('open', () => {
        console.log('[TEST] WebSocket connected');
        resolve();
      });

      this.ws.on('message', (data) => {
        this.handleMessage(data);
      });

      this.ws.on('error', (err) => {
        console.error('[TEST] WebSocket error:', err);
        this.errors.push(err);
        reject(err);
      });

      setTimeout(() => reject(new Error('Connection timeout')), 5000);
    });
  }

  handleMessage(data) {
    try {
      // Parse frame header (simplified - just check if it's binary)
      if (data instanceof ArrayBuffer) {
        const view = new DataView(data);
        const version = view.getUint8(0);
        const msgType = view.getUint8(1);

        if (msgType === 0x02) { // CONTROL frame
          const payloadLen = view.getUint16(10, false);
          const payloadBytes = new Uint8Array(data, 16, payloadLen);
          const payloadText = new TextDecoder().decode(payloadBytes);
          const payload = JSON.parse(payloadText);

          console.log('[TEST] Received frame:', payload.type);
          this.receivedFrames.push(payload);

          // Auto-respond to control frames
          this.handleControlFrame(payload);
        }
      }
    } catch (err) {
      console.error('[TEST] Error handling message:', err);
      this.errors.push(err);
    }
  }

  handleControlFrame(payload) {
    if (payload.type === 'session_ready') {
      // Extract first word and send play_word
      if (payload.words && payload.words.length > 0) {
        const firstWord = payload.words[0];
        console.log('[TEST] Sending play_word for:', firstWord);
        this.sendControlFrame({
          type: 'play_word',
          params: { word_id: firstWord }
        });
      }
    } else if (payload.type === 'word_complete') {
      // Count completed words and send next_word
      const completedCount = this.receivedFrames.filter(f => f.type === 'word_complete').length;
      console.log('[TEST] Word complete, total:', completedCount);

      if (completedCount < 5) { // Test 5 words to save time
        // Get next word from session_ready
        const sessionReady = this.receivedFrames.find(f => f.type === 'session_ready');
        if (sessionReady && sessionReady.words && completedCount < sessionReady.words.length) {
          const nextWord = sessionReady.words[completedCount];
          console.log('[TEST] Sending next_word for:', nextWord);
          this.sendControlFrame({
            type: 'next_word',
            params: { word_id: nextWord }
          });
        }
      } else {
        console.log('[TEST] Test complete - received 5 word_complete frames');
        setTimeout(() => this.ws.close(), 500);
      }
    } else if (payload.type === 'progress_update') {
      console.log('[TEST] Progress update:', payload);
    }
  }

  sendControlFrame(payload) {
    const json = JSON.stringify(payload);
    // Simplified frame encoding (just send as JSON for this test)
    this.ws.send(json);
  }

  async runTest() {
    try {
      await this.startServer();
      await this.connect();

      // Send start_session
      console.log('[TEST] Sending start_session...');
      this.sendControlFrame({
        type: 'start_session',
        params: { limit: 30 }
      });

      // Wait for test to complete or timeout
      await new Promise((resolve, reject) => {
        const timeout = setTimeout(() => {
          if (this.receivedFrames.length > 0) {
            resolve();
          } else {
            reject(new Error('Test timeout - no frames received'));
          }
        }, this.timeoutMs);

        this.ws.on('close', () => {
          clearTimeout(timeout);
          resolve();
        });
      });

      return this.analyzeResults();

    } finally {
      await this.stopServer();
    }
  }

  analyzeResults() {
    console.log('\n=== TEST RESULTS ===');
    console.log('Total frames received:', this.receivedFrames.length);
    console.log('Errors:', this.errors.length);

    const sessionReady = this.receivedFrames.find(f => f.type === 'session_ready');
    const wordCompletes = this.receivedFrames.filter(f => f.type === 'word_complete');
    const progressUpdates = this.receivedFrames.filter(f => f.type === 'progress_update');

    console.log('\n--- Frame counts ---');
    console.log('session_ready:', sessionReady ? 1 : 0);
    console.log('word_complete:', wordCompletes.length);
    console.log('progress_update:', progressUpdates.length);

    // Verify fixes
    const results = {
      passed: true,
      issues: []
    };

    // Check 1: session_ready received (Bug 1: "connection error")
    if (!sessionReady) {
      results.issues.push('❌ Bug 1 NOT FIXED: No session_ready received (likely "connection error")');
      results.passed = false;
    } else {
      console.log('✅ Bug 1 FIXED: session_ready received successfully');
    }

    // Check 2: Multiple word_complete frames (Bug 2: only one word)
    if (wordCompletes.length < 2) {
      results.issues.push(`❌ Bug 2 NOT FIXED: Only ${wordCompletes.length} word_complete(s) received, expected multiple`);
      results.passed = false;
    } else {
      console.log(`✅ Bug 2 FIXED: ${wordCompletes.length} word_complete frames received`);
    }

    // Check 3: Progress frames use camelCase (Bug 1 field name fix)
    if (progressUpdates.length > 0) {
      const firstProgress = progressUpdates[0];
      if (firstProgress.currentWord !== undefined && firstProgress.currentWord !== null) {
        console.log('✅ Bug 1 field names FIXED: progress_update uses camelCase (currentWord)');
      } else if (firstProgress.current_word !== undefined) {
        results.issues.push('❌ Bug 1 field names NOT FIXED: progress_update still uses snake_case (current_word)');
        results.passed = false;
      }
    }

    console.log('\n=== FINAL RESULT ===');
    if (results.passed) {
      console.log('✅ ALL TESTS PASSED');
    } else {
      console.log('❌ TESTS FAILED:');
      results.issues.forEach(issue => console.log(issue));
    }

    return results;
  }
}

// Run test
(async () => {
  const test = new E2ETest();
  try {
    const results = await test.runTest();
    process.exit(results.passed ? 0 : 1);
  } catch (err) {
    console.error('[TEST] Fatal error:', err);
    process.exit(1);
  }
})();
