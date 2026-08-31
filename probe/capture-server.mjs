import http from 'node:http';
import fs from 'node:fs';

const PORT = 17823;
const LOG = './capture.log';
fs.writeFileSync(LOG, '');

const server = http.createServer(async (req, res) => {
  const chunks = [];
  req.on('data', c => chunks.push(c));
  req.on('end', () => {
    const body = Buffer.concat(chunks).toString();
    const headers = req.headers;
    const entry = {
      time: new Date().toISOString(),
      method: req.method,
      url: req.url,
      headers,
      body: body.slice(0, 8000),
    };
    const line = JSON.stringify(entry, null, 2);
    console.log('\n=== CAPTURED REQUEST ===');
    console.log(line);
    fs.appendFileSync(LOG, line + '\n---\n');

    if (req.url.includes('/v1/messages') || req.url.includes('/v1/oauth')) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        id: 'msg_mock_123',
        type: 'message',
        role: 'assistant',
        model: 'claude-sonnet-4-20250514',
        content: [{ type: 'text', text: 'probe ok' }],
        stop_reason: 'end_turn',
        usage: { input_tokens: 10, output_tokens: 5 }
      }));
    } else {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, probe: true }));
    }
  });
});

server.listen(PORT, () => console.log(`probe listening http://localhost:${PORT}`));
