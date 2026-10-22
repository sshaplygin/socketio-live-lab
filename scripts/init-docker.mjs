import { randomBytes } from 'node:crypto';
import { writeFile } from 'node:fs/promises';

const file = new URL('../.env.docker', import.meta.url);
const content = [
  '# Local Docker configuration. Generated secrets must stay out of git.',
  'LAB_HTTP_PORT=8080',
  'LAB_ALLOWED_ORIGINS=http://localhost:4321,http://127.0.0.1:4321',
  `LAB_BOT_TOKEN=${randomBytes(32).toString('hex')}`,
  `LAB_METRICS_TOKEN=${randomBytes(32).toString('hex')}`,
  '',
].join('\n');

try {
  await writeFile(file, content, { flag: 'wx', mode: 0o600 });
  console.log('Created .env.docker with independent random secrets.');
} catch (error) {
  if (error.code !== 'EEXIST') throw error;
  console.log('Keeping existing .env.docker unchanged.');
}
