import { test } from 'node:test';
import assert from 'node:assert/strict';
import mitt from './src/index.ts';

test('off with an unregistered handler removes nothing', () => {
	const e = mitt<{ a: number }>();
	const got: string[] = [];
	e.on('a', () => got.push('first'));
	e.on('a', () => got.push('second'));
	e.off('a', () => {});
	e.emit('a', 1);
	assert.deepEqual(got, ['first', 'second']);
});

test('off removes exactly the given handler', () => {
	const e = mitt<{ a: number }>();
	const got: string[] = [];
	const one = () => got.push('one');
	e.on('a', one);
	e.on('a', () => got.push('two'));
	e.off('a', one);
	e.emit('a', 1);
	assert.deepEqual(got, ['two']);
});
