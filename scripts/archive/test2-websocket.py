import asyncio
import websockets
import json

async def test_start_session():
    uri = "ws://localhost:8080/ws"
    try:
        async with websockets.connect(uri) as ws:
            print("WebSocket connected")
            
            # Send start_session control frame
            msg = {
                "type": "start_session",
                "params": {"limit": 5}
            }
            
            # Encode as control frame (type byte 0x02 + JSON)
            json_str = json.dumps(msg)
            frame = bytes([0x02]) + json_str.encode('utf-8')
            
            print(f"Sending: {json_str}")
            await ws.send(frame)
            
            # Wait for response
            print("Waiting for response...")
            response = await asyncio.wait_for(ws.recv(), timeout=10)
            
            print(f"Received {len(response)} bytes")
            msg_type = response[0]
            print(f"Message type byte: {msg_type}")
            
            if msg_type == 0x02:
                # Control frame
                payload = response[1:].decode('utf-8')
                print(f"Control frame: {payload}")
            elif msg_type == 0x03:
                # Audio frame
                print(f"Audio frame, payload length: {len(response) - 1}")
            elif msg_type == 0x04:
                # Error frame
                payload = response[1:].decode('utf-8')
                print(f"Error frame: {payload}")
                
    except Exception as e:
        print(f"Error: {e}")

asyncio.run(test_start_session())
