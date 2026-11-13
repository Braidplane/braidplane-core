const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createRequire } = require('node:module');
const { spawnSync } = require('node:child_process');
const dependency = createRequire(require.resolve('@usebruno/cli/package.json'));

test('multipart field names and filenames cannot inject MIME headers', () => {
    const FormData = dependency('form-data');
    for (const [field, options] of [
        ['email"\r\nX-Injected: true\r\nfake="', {}],
        ['attachment', { filename: 'file"\r\nX-Injected: true\r\nfake=".txt' }],
    ]) {
        const form = new FormData();
        form.append(field, Buffer.from('synthetic-content'), options);
        const body = form.getBuffer().toString();
        assert.ok(!body.includes('\r\nX-Injected: true'));
        assert.ok(body.includes('synthetic-content'));
    }
    const ordinary = new FormData();
    ordinary.append('attachment', Buffer.from('content'), { filename: 'fixture.txt' });
    assert.ok(ordinary.getBuffer().toString().includes('filename="fixture.txt"'));
});

test('Faker templates cannot evaluate constructor expressions', () => {
    const { faker } = dependency('@faker-js/faker');
    try {
        faker.rawDefinitions.test = () => () => {};
        for (const property of ['constructor', 'prototype.constructor']) {
            globalThis.__brunoFakerInjection = false;
            try {
                faker.helpers.fake(`{{test.${property}(globalThis.__brunoFakerInjection = true)}}`);
            } catch {}
            assert.equal(globalThis.__brunoFakerInjection, false);
        }
        assert.equal(typeof faker.number.int({ min: 1, max: 3 }), 'number');
        assert.equal(typeof faker.number.float({ min: 0, max: 1000, precision: 0.01 }), 'number');
        assert.ok(faker.helpers.fake('{{person.firstName}}').length > 0);
        assert.ok(faker.string.alpha({ length: { min: 5, max: 10 } }).length >= 5);
        assert.doesNotThrow(() => dependency('@usebruno/requests'));
    } finally {
        delete faker.rawDefinitions.test;
        delete globalThis.__brunoFakerInjection;
    }
});

test('CSV duplicate prototype headers stay data and CLI iteration parsing still works', async () => {
    const { parse } = dependency('csv-parse/sync');
    const [record] = parse('__proto__,__proto__\na,b\n', { columns: true, group_columns_by_name: true });
    assert.equal(Object.getPrototypeOf(record), Object.prototype);
    assert.deepEqual(Object.getOwnPropertyDescriptor(record, '__proto__').value, ['a', 'b']);
    const { parseCSV } = dependency('./src/utils/filesystem');
    assert.deepEqual(await parseCSV('\uFEFFname,email\n\n Alice , alice@example.test \n'),
        [{ name: 'Alice', email: 'alice@example.test' }]);
});

test('Axios ignores inherited HTTP methods while retaining explicit methods', async () => {
    const axios = dependency('axios');
    const adapter = async config => ({ data: config.method, status: 200, statusText: 'OK', headers: {}, config });
    try {
        Object.prototype.method = 'DELETE';
        assert.equal((await axios.request({ url: 'http://127.0.0.1/unused', adapter })).data, 'get');
        assert.equal((await axios.request({ url: 'http://127.0.0.1/unused', method: 'POST', adapter })).data, 'post');
    } finally {
        delete Object.prototype.method;
    }
});

test('UUID CommonJS APIs reject undersized output buffers', () => {
    const uuid = dependency('uuid');
    const value = uuid.v4();
    assert.ok(uuid.validate(value));
    assert.equal(uuid.stringify(uuid.parse(value)), value);
    assert.equal(uuid.version(uuid.v7()), 7);
    assert.throws(() => uuid.v5('fixture', uuid.v5.URL, new Uint8Array(15)), RangeError);
    assert.throws(() => uuid.v5('fixture', uuid.v5.URL, new Uint8Array(16), -1), RangeError);
});

test('Nanoid invalid sizes terminate and ordinary custom IDs still work', () => {
    const nanoid = dependency('nanoid');
    assert.match(nanoid.customAlphabet('abcdef', 12)(), /^[abcdef]{12}$/);
    for (const expression of ['require("nanoid/non-secure").nanoid(-1)', 'require("nanoid/non-secure").customAlphabet("ab", 0)()']) {
        const child = spawnSync(process.execPath, ['-e', `try { ${expression}; } catch (e) { if (!(e instanceof RangeError)) process.exit(1); }`],
            { cwd: __dirname, timeout: 1500, encoding: 'utf8' });
        assert.equal(child.error, undefined);
        assert.equal(child.status, 0, child.stderr);
    }
});
