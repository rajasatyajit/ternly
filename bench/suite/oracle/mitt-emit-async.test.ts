import { test } from 'node:test';
import assert from 'node:assert/strict';
import mitt from './src/index.ts';

const tick = (ms: number) => new Promise((r) => setTimeout(r, ms));

test('emitAsync awaits each handler in order, then wildcards', async () => {
	const e = mitt<{ a: number }>();
	const log: string[] = [];
	e.on('a', async (n) => { await tick(20); log.push('slow ' + n); });
	e.on('a', (n) => { log.push('fast ' + n); });
	e.on('*', async (type, n) => { await tick(5); log.push('wild ' + String(type) + n); });
	const n = await e.emitAsync('a', 1);
	assert.equal(n, 3);
	assert.deepEqual(log, ['slow 1', 'fast 1', 'wild a1']);
});

test('emitAsync with no handlers resolves to 0', async () => {
	const e = mitt<{ a: number }>();
	assert.equal(await e.emitAsync('a', 1), 0);
});

test('a rejection stops the chain', async () => {
	const e = mitt<{ a: number }>();
	const log: string[] = [];
	e.on('a', async () => { throw new Error('boom'); });
	e.on('a', () => { log.push('after'); });
	await assert.rejects(e.emitAsync('a', 1), /boom/);
	assert.deepEqual(log, []);
});

test('sync emit still works', () => {
	const e = mitt<{ a: number }>();
	let got = 0;
	e.on('a', (n) => { got = n; });
	e.emit('a', 5);
	assert.equal(got, 5);
});
