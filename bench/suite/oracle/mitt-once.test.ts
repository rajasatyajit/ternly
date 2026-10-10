import { test } from 'node:test';
import assert from 'node:assert/strict';
import mitt from './src/index.ts';

test('once fires a single time', () => {
	const e = mitt<{ a: number }>();
	const got: number[] = [];
	e.once('a', (n) => got.push(n));
	e.emit('a', 1);
	e.emit('a', 2);
	assert.deepEqual(got, [1]);
});

test('once handlers coexist with on handlers', () => {
	const e = mitt<{ a: number }>();
	const got: string[] = [];
	e.on('a', () => got.push('on'));
	e.once('a', () => got.push('once'));
	e.emit('a', 1);
	e.emit('a', 2);
	assert.deepEqual(got, ['on', 'once', 'on']);
});

test('off cancels a pending once', () => {
	const e = mitt<{ a: number }>();
	let n = 0;
	const h = () => { n++; };
	e.once('a', h);
	e.off('a', h);
	e.emit('a', 1);
	assert.equal(n, 0);
});
