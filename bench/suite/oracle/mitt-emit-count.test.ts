import { test } from 'node:test';
import assert from 'node:assert/strict';
import mitt from './src/index.ts';

test('emit returns the number of handlers called', () => {
	const e = mitt<{ a: number; b: string }>();
	assert.equal(e.emit('a', 1), 0);
	e.on('a', () => {});
	e.on('a', () => {});
	assert.equal(e.emit('a', 1), 2);
	e.on('*', () => {});
	assert.equal(e.emit('a', 1), 3);
	assert.equal(e.emit('b', 'x'), 1);
});
