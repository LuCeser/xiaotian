const WebSocket = require('ws');

const ws = new WebSocket('ws://localhost:8080/ws', {
    origin: 'http://localhost',
    headers: {
        'Origin': 'http://localhost'
    }
});

ws.on('open', () => {
    console.log('WebSocket connected');
    
    // Send start_session control frame
    const msg = {
        type: 'start_session',
        params: { limit: 5 }
    };
    
    // Encode as control frame (type byte 0x02 + JSON)
    const jsonStr = JSON.stringify(msg);
    const buffer = Buffer.from(jsonStr, 'utf8');
    const frame = Buffer.concat([Buffer.from([0x02]), buffer]);
    
    console.log('Sending start_session:', jsonStr);
    ws.send(frame);
});

ws.on('message', (data) => {
    console.log('Received message, length:', data.length);
    console.log('First byte (type):', data[0]);
    
    if (data[0] === 0x02) {
        // Control frame
        const json = data.slice(1).toString('utf8');
        console.log('Control frame:', json);
    } else if (data[0] === 0x03) {
        // Audio frame
        console.log('Audio frame, payload length:', data.length - 1);
    } else if (data[0] === 0x04) {
        // Error frame
        const json = data.slice(1).toString('utf8');
        console.log('Error frame:', json);
    }
});

ws.on('error', (err) => {
    console.error('WebSocket error:', err.message);
});

ws.on('close', () => {
    console.log('WebSocket closed');
    process.exit(0);
});

// Timeout after 10 seconds
setTimeout(() => {
    console.log('Test timeout - closing');
    ws.close();
}, 10000);
