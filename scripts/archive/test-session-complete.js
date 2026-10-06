const WebSocket = require('ws');

// Test configuration
const TEST_CONFIG = {
    url: 'ws://localhost:8080/ws',
    origin: 'http://localhost', // Required origin for WebSocket upgrade
    wordLimit: 3, // Use 3 words for faster testing
    timeout: 30000 // 30 seconds timeout
};

// Frame tracking
const frameLog = [];
let sessionReadyReceived = false;
let wordCount = 0;
let completedWords = 0;
let sessionCompleteReceived = false;
let lastProgressUpdate = null;

function sendControlFrame(ws, type, params = {}) {
    const msg = { type, params };
    const jsonStr = JSON.stringify(msg);
    const buffer = Buffer.from(jsonStr, 'utf8');
    const frame = Buffer.concat([Buffer.from([0x02]), buffer]);

    console.log(`[TX] Control frame: ${type}`, params);
    ws.send(frame);
}

function sendReviewFrame(ws, ratings) {
    const msg = {
        type: 'submit_review',
        params: { ratings }
    };
    const jsonStr = JSON.stringify(msg);
    const buffer = Buffer.from(jsonStr, 'utf8');
    const frame = Buffer.concat([Buffer.from([0x02]), buffer]);

    console.log('[TX] Submit review with ratings:', ratings);
    ws.send(frame);
}

function logFrame(type, data) {
    const timestamp = new Date().toISOString();
    frameLog.push({ timestamp, type, data });
    console.log(`[RX] ${type}:`, data);
}

function runTest() {
    console.log('=== Starting session_complete end-to-end test ===');
    console.log(`Target: ${TEST_CONFIG.url}`);
    console.log(`Origin: ${TEST_CONFIG.origin}`);
    console.log(`Word limit: ${TEST_CONFIG.wordLimit}`);

    const ws = new WebSocket(TEST_CONFIG.url, {
        origin: TEST_CONFIG.origin,
        headers: {
            'Origin': TEST_CONFIG.origin
        }
    });

    ws.on('open', () => {
        console.log('[CONNECT] WebSocket connected successfully');

        // Start session
        setTimeout(() => {
            sendControlFrame(ws, 'start_session', {
                limit: TEST_CONFIG.wordLimit,
                category: 'english' // Use 'english' category that has due cards
            });
        }, 500);
    });

    ws.on('message', (data) => {
        if (data.length < 1) return;

        const frameType = data[0];
        const payload = data.slice(1);

        try {
            // Control frames (0x02)
            if (frameType === 0x02) {
                const json = JSON.parse(payload.toString('utf8'));
                handleControlFrame(ws, json);
            }
            // Audio frames (0x03)
            else if (frameType === 0x03) {
                logFrame('AUDIO', { size: payload.length });
            }
            // Error frames (0x04)
            else if (frameType === 0x04) {
                const json = JSON.parse(payload.toString('utf8'));
                logFrame('ERROR', json);
            }
            else {
                logFrame('UNKNOWN', { frameType, size: payload.length });
            }
        } catch (error) {
            console.error('[ERROR] Failed to parse frame:', error.message);
        }
    });

    ws.on('error', (err) => {
        console.error('[ERROR] WebSocket error:', err.message);
        process.exit(1);
    });

    ws.on('close', () => {
        console.log('[DISCONNECT] WebSocket closed');
        printResults();
    });

    // Test timeout
    setTimeout(() => {
        console.log('[TIMEOUT] Test timed out after', TEST_CONFIG.timeout/1000, 'seconds');
        ws.close();
        printResults();
        process.exit(1);
    }, TEST_CONFIG.timeout);
}

function handleControlFrame(ws, frame) {
    logFrame('CONTROL', frame);

    switch (frame.type) {
        case 'session_ready':
            sessionReadyReceived = true;
            wordCount = frame.words ? frame.words.length : 0;
            console.log(`[INFO] Session ready with ${wordCount} words`);
            break;

        case 'progress_update':
            lastProgressUpdate = frame;
            completedWords = frame.completed_words || 0;
            console.log(`[INFO] Progress: ${completedWords}/${frame.total_words} (current word: ${frame.current_word})`);

            // Check if all words are completed
            if (completedWords >= frame.total_words) {
                console.log('[INFO] All words completed, sending review...');

                // Send review after a short delay
                setTimeout(() => {
                    sendReviewFrame(ws, {
                        'word1': 'good',
                        'word2': 'good',
                        'word3': 'good'
                    });
                }, 1000);
            }
            break;

        case 'word_complete':
            console.log(`[INFO] Word completed: ${frame.word_id}`);
            break;

        case 'session_complete':
            sessionCompleteReceived = true;
            console.log('[SUCCESS] ✅ session_complete frame received!');
            console.log(`[INFO] Session completed: ${frame.total_words} words in ${frame.duration_ms}ms`);

            // Close connection after receiving session_complete
            setTimeout(() => {
                console.log('[INFO] Test successful, closing connection...');
                ws.close();
            }, 500);
            break;

        case 'submit_review_result':
            console.log('[INFO] Review submitted successfully:', frame.result);
            break;

        default:
            console.log(`[INFO] Unhandled control frame type: ${frame.type}`);
    }
}

function printResults() {
    console.log('\n=== Test Results ===');
    console.log(`Session ready received: ${sessionReadyReceived ? '✅' : '❌'}`);
    console.log(`Total words in session: ${wordCount}`);
    console.log(`Words completed: ${completedWords}`);
    console.log(`Session complete received: ${sessionCompleteReceived ? '✅' : '❌'}`);

    if (sessionCompleteReceived) {
        console.log('\n🎉 TEST PASSED: session_complete frame is working correctly!');
        console.log('The EventParentReview wire-up fix is verified.');
    } else {
        console.log('\n❌ TEST FAILED: session_complete frame was NOT received');
        console.log('Expected timeline: session_ready → progress_update(N) → submit_review → session_complete');
    }

    console.log('\n=== Frame Log Summary ===');
    console.log(`Total frames received: ${frameLog.length}`);
    const frameTypes = {};
    frameLog.forEach(log => {
        frameTypes[log.type] = (frameTypes[log.type] || 0) + 1;
    });
    Object.entries(frameTypes).forEach(([type, count]) => {
        console.log(`  ${type}: ${count}`);
    });

    process.exit(sessionCompleteReceived ? 0 : 1);
}

// Start the test
runTest();