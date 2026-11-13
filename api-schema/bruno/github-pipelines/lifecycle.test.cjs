const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {createHash} = require('node:crypto');
const {runInNewContext} = require('node:vm');
const {config, fixture, assertReceipt, receiptEvidence, run} = require('./lifecycle.cjs');

const target = {
  run_id: 'bp-934-20261013-123456abcdef', repository: 'fixture/test-pipelines',
  base_branch: 'bp-934-e2e-base', organization: 'ai-agent-s-workspace', environment: 'dev',
  template_commit: 'a'.repeat(40),
  installation_id: '1', app_id: '2', actor: 'fixture[bot]', server_url: 'https://app.staging.example.test',
};
const environment = {
  BRAIDPLANE_GITHUB_E2E_CONFIG: JSON.stringify(target),
  BRAIDPLANE_GITHUB_E2E_TOKEN: 'fixture-installation-token',
  BRAIDPLANE_GITHUB_E2E_APP_JWT: 'fixture-app-jwt',
  BRAIDPLANE_GITHUB_E2E_PAT: 'fixture-user-token',
  BRAIDPLANE_GITHUB_E2E_API_KEY: 'fixture-api-key',
};

test('configuration rejects ordinary repositories, default bases, ambiguous targets and non-installation actors', () => {
  assert.equal(config(key => environment[key]).server_url, target.server_url);
  for (const change of [{repository: 'customer/production'}, {base_branch: 'main'}, {run_id: 'bp-934'},
    {template_commit: undefined}, {template_commit: 'a'.repeat(39)}, {template_commit: 'A'.repeat(40)},
    {organization: '../other'}, {installation_id: ''}, {actor: 'human'}, {server_url: 'https://user:password@example.test'},
    {server_url: 'http://localhost'}, {server_url: 'https://example.test/api'}]) {
    assert.throws(() => config(key => key === 'BRAIDPLANE_GITHUB_E2E_CONFIG' ? JSON.stringify({...target, ...change}) : environment[key]));
  }
  assert.throws(() => config(key => key === 'BRAIDPLANE_GITHUB_E2E_APP_JWT' ? undefined : environment[key]), /task_owned_credential_required/);
});

test('a template SHA mismatch fails before any provider or staging mutation', async () => {
  const cfg = config(key => environment[key]);
  cfg.run_id = 'bp-934-20261013-eeeeeeeeeeee';
  const journal = path.resolve(__dirname, '../../../.worktree/bp-934', `${cfg.run_id}.json`);
  assert.equal(fs.existsSync(journal), false);
  const request = async req => {
    assert.equal(req.method, 'GET', 'no mutation before the configured template matches');
    const url = new URL(req.url);
    if (url.pathname === '/repos/fixture/test-pipelines') {
      return {status: 200, data: {id: 42, full_name: cfg.repository, private: true, default_branch: 'main'}};
    }
    assert.ok(url.pathname.endsWith('/branches/bp-934-e2e-base'));
    return {status: 200, data: {protected: false, commit: {sha: 'b'.repeat(40)}}};
  };
  try {
    await assert.rejects(run(cfg, request, {status: 200, data: {id: 1, app_id: 2}}), /template_commit_mismatch/);
    const saved = JSON.parse(fs.readFileSync(journal));
    assert.equal(saved.outcome, 'failed');
    assert.deepEqual(saved.cleanup.failures, []);
  } finally { fs.rmSync(journal, {force: true}); }
});

test('synthetic fixture connects its run-scoped input without a connector', () => {
  const spec = fixture(target.run_id);
  assert.equal(spec.metadata.slug, target.run_id);
  assert.equal(spec.steps.length, 2);
  assert.equal(spec.steps[0].kind, 'input');
  assert.equal(spec.steps[1].transform.inputs.input, spec.steps[0].slug);
  assert.equal(JSON.stringify(spec).includes('data_source'), false);
});

test('receipts cannot be transferred across exact source identities', () => {
  const state = {repository_id: 42, pull: 7, base: `${target.run_id}-base`, pipeline_path: 'pipelines/fixture.pipeline.yaml'};
  const head = 'a'.repeat(40), hash = 'b'.repeat(64);
  const receipt = {candidate: {organization_slug: target.organization, project_slug: target.run_id,
    environment_slug: target.environment, provider_connection_slug: `${target.run_id}-git`, repository_id: '42',
    change_request_id: '7', head_sha: head, target_ref: `refs/heads/${state.base}`, pipeline_path: state.pipeline_path,
    spec_hash: `sha256:${hash}`}, claims: [{}]};
  assertReceipt(receipt, target, state, head, hash);
  for (const key of ['organization_slug', 'project_slug', 'environment_slug', 'repository_id', 'change_request_id',
    'head_sha', 'target_ref', 'pipeline_path', 'spec_hash', 'provider_connection_slug']) {
    assert.throws(() => assertReceipt({...receipt, candidate: {...receipt.candidate, [key]: 'different'}}, target, state, head, hash));
  }
});

test('cleanup cannot claim success without its ownership journal', async () => {
  const cfg = config(key => environment[key]);
  cfg.run_id = 'bp-934-20261013-cccccccccccc';
  cfg.cleanup_only = true;
  const journal = path.resolve(__dirname, '../../../.worktree/bp-934', `${cfg.run_id}.json`);
  assert.equal(fs.existsSync(journal), false);
  await assert.rejects(run(cfg, () => assert.fail('no provider mutation without ownership'),
    {status: 200, data: {id: 1, app_id: 2}}), /cleanup_ownership_journal_required/);
  assert.equal(fs.existsSync(journal), false);
});

test('failed journal writes preserve the previous journal and remove temporary files', async () => {
  const cfg = config(key => environment[key]);
  cfg.run_id = 'bp-934-20261013-dddddddddddd';
  const journal = path.resolve(__dirname, '../../../.worktree/bp-934', `${cfg.run_id}.json`);
  assert.equal(fs.existsSync(journal), false);
  const previous = JSON.stringify({run_id: cfg.run_id, target: {
    server_url: cfg.server_url, organization: cfg.organization, environment: cfg.environment, repository: cfg.repository}, sentinel: 'previous'});
  fs.mkdirSync(path.dirname(journal), {recursive: true});
  const write = fs.writeFileSync, rename = fs.renameSync;
  try {
    for (const operation of ['writeFileSync', 'renameSync']) {
      write(journal, previous);
      fs[operation] = (...args) => {
        if (operation === 'writeFileSync') write(...args);
        throw new Error('journal_io_failure');
      };
      try {
        await assert.rejects(run(cfg, () => assert.fail('no provider mutation after journal failure'),
          {status: 200, data: {id: 1, app_id: 2}}), /journal_io_failure/);
      } finally {
        fs.writeFileSync = write;
        fs.renameSync = rename;
      }
      assert.equal(fs.readFileSync(journal, 'utf8'), previous);
      assert.equal(fs.existsSync(`${journal}.tmp`), false);
    }
  } finally {
    fs.writeFileSync = write;
    fs.renameSync = rename;
    fs.rmSync(journal, {force: true});
    fs.rmSync(`${journal}.tmp`, {force: true});
  }
});

test('partial setup failure retains sanitized evidence and retries exact scoped cleanup', async () => {
  const cfg = config(key => environment[key]);
  cfg.run_id = 'bp-934-20261013-987654abcdef';
  const journal = path.resolve(__dirname, '../../../.worktree/bp-934', `${cfg.run_id}.json`);
  assert.equal(fs.existsSync(journal), false);
  const calls = [];
  const request = async req => {
    calls.push(req);
    const url = new URL(req.url);
    assert.equal(req.maxRedirects, 0);
    assert.equal(req.timeout, 30000);
    if (url.pathname === '/repos/fixture/test-pipelines') return {status: 200, data: {id: 42, full_name: cfg.repository, private: true, default_branch: 'main'}};
    if (url.pathname.endsWith('/branches/bp-934-e2e-base')) return {status: 200, data: {protected: false, commit: {sha: cfg.template_commit}}};
    if (url.pathname.includes('/git/commits/')) return {status: 200, data: {tree: {sha: 'b'.repeat(40)}}};
    if (url.pathname.endsWith('/git/trees')) return {status: 201, data: {sha: 'c'.repeat(40)}};
    if (url.pathname.endsWith('/git/commits')) return {status: 201, data: {sha: 'd'.repeat(40)}};
    if (req.method === 'POST' && url.pathname.endsWith('/git/refs')) throw new Error(`raw provider response ${cfg.token}`);
    if (url.pathname.endsWith('/pulls')) return {status: 200, data: []};
    return {status: 404, data: {}};
  };
  try {
    await assert.rejects(run(cfg, request, {status: 200, data: {id: 1, app_id: 2}}), /provider_or_network_unavailable/);
    const saved = fs.readFileSync(journal, 'utf8');
    assert.equal(saved.includes(cfg.token), false);
    assert.equal(saved.includes(cfg.pat), false);
    assert.equal(saved.includes(cfg.api_key), false);
    assert.equal(JSON.parse(saved).failure, 'provider_or_network_unavailable');
    assert.deepEqual(JSON.parse(saved).cleanup.failures, []);
    assert.equal(calls.some(req => req.method === 'DELETE'), false, 'absent resources need no delete');
    cfg.cleanup_only = true;
    await run(cfg, request, {status: 200, data: {id: 1, app_id: 2}});
    assert.deepEqual(JSON.parse(fs.readFileSync(journal)).cleanup.failures, []);
    const templateWrites = calls.filter(req => req.method !== 'GET' && req.url.includes('bp-934-e2e-base'));
    assert.deepEqual(templateWrites, []);
  } finally {
    fs.rmSync(journal, {force: true});
  }
});

test('cleanup continues past an unowned proposal while reporting its failure', async () => {
  const cfg = config(key => environment[key]);
  cfg.run_id = 'bp-934-20261013-bbbbbbbbbbbb';
  cfg.cleanup_only = true;
  const journal = path.resolve(__dirname, '../../../.worktree/bp-934', `${cfg.run_id}.json`);
  assert.equal(fs.existsSync(journal), false);
  const base = `${cfg.run_id}-base`, branch = `braidplane/${cfg.run_id}/proposal`;
  const marker = {run_id: cfg.run_id, attempt_id: 'fixture-attempt'};
  fs.mkdirSync(path.dirname(journal), {recursive: true});
  fs.writeFileSync(journal, JSON.stringify({run_id: cfg.run_id,
    target: {server_url: cfg.server_url, organization: cfg.organization, environment: cfg.environment, repository: cfg.repository},
    base, base_created: true, repository_id: 42, attempt_id: marker.attempt_id, branches: [base]}));
  const mutations = [];
  const request = async req => {
    const p = new URL(req.url).pathname;
    if (req.method !== 'GET') { mutations.push(p); return {status: req.method === 'DELETE' ? 204 : 200, data: {}}; }
    if (p.endsWith('/pulls')) return {status: 200, data: [
      {number: 6, state: 'open', base: {ref: base}, head: {ref: 'unowned', repo: {id: 99}}},
      {number: 7, state: 'open', base: {ref: base}, head: {ref: branch, repo: {id: 42}}},
    ]};
    if (p.includes('/contents/.braidplane-e2e/')) return {status: 200, data: {content: Buffer.from(JSON.stringify(marker)).toString('base64')}};
    if (p.includes('/git/ref/heads/')) return {status: 200, data: {object: {sha: 'a'.repeat(40)}}};
    return {status: 404, data: {}};
  };
  try {
    await assert.rejects(run(cfg, request, {status: 200, data: {id: 1, app_id: 2}}), /scoped_cleanup_incomplete/);
    assert.deepEqual(JSON.parse(fs.readFileSync(journal)).cleanup.failures, ['proposal']);
    assert.equal(JSON.parse(fs.readFileSync(journal)).outcome, 'failed');
    assert.ok(mutations.includes('/repos/fixture/test-pipelines/pulls/7'));
    assert.ok(mutations.includes(`/repos/fixture/test-pipelines/git/refs/heads/${branch}`));
    assert.equal(mutations.some(p => p.endsWith('/pulls/6') || p.endsWith('/heads/unowned')), false);
  } finally { fs.rmSync(journal, {force: true}); }
});

test('cleanup retries closed proposals after branch deletion using exact-head ownership', async () => {
  const cfg = {...config(key => environment[key]), run_id: 'bp-934-20261013-cccccccccccc', cleanup_only: true};
  const journal = path.resolve(__dirname, '../../../.worktree/bp-934', `${cfg.run_id}.json`);
  const base = `${cfg.run_id}-base`, branch = `braidplane/${cfg.run_id}/proposal`, head = 'c'.repeat(40);
  const marker = {run_id: cfg.run_id, attempt_id: 'owned-attempt'};
  fs.mkdirSync(path.dirname(journal), {recursive: true});
  assert.equal(fs.existsSync(journal), false);
  fs.writeFileSync(journal, JSON.stringify({run_id: cfg.run_id,
    target: {server_url: cfg.server_url, organization: cfg.organization, environment: cfg.environment, repository: cfg.repository},
    base, base_created: true, repository_id: 42, attempt_id: marker.attempt_id, branches: [base, branch]}));
  const mutations = [];
  const request = async req => {
    const url = new URL(req.url);
    if (req.method !== 'GET') {mutations.push(req.url); return {status: 204, data: {}};}
    if (url.pathname.endsWith('/pulls')) return {status: 200, data: [
      {number: 7, state: 'closed', base: {ref: base}, head: {ref: branch, sha: head, repo: {id: 42}}},
    ]};
    if (url.pathname.includes('/contents/.braidplane-e2e/') && url.searchParams.get('ref') === head)
      return {status: 200, data: {content: Buffer.from(JSON.stringify(marker)).toString('base64')}};
    return {status: 404, data: {}};
  };
  try {
    for (let retry = 0; retry < 2; retry++) {
      await run(cfg, request, {status: 200, data: {id: 1, app_id: 2}});
      assert.deepEqual(JSON.parse(fs.readFileSync(journal)).cleanup.failures, []);
    }
    marker.attempt_id = 'other-attempt';
    await assert.rejects(run(cfg, request, {status: 200, data: {id: 1, app_id: 2}}), /scoped_cleanup_incomplete/);
    assert.deepEqual(mutations, [], 'deleted or unowned artifacts must never be mutated');
  } finally {fs.rmSync(journal, {force: true});}
});

test('complete lifecycle runs in the Bruno sandbox and deletes only owned artifacts', async () => {
  const run = runInNewContext(fs.readFileSync(path.join(__dirname, 'lifecycle.cjs'), 'utf8') + '\nmodule.exports.run',
    {module: {exports: {}}, require, __dirname, Buffer, URL, setTimeout});
  const cfg = config(key => environment[key]);
  cfg.run_id = 'bp-934-20261013-aaaaaaaaaaaa';
  const journal = path.resolve(__dirname, '../../../.worktree/bp-934', `${cfg.run_id}.json`);
  assert.equal(fs.existsSync(journal), false);
  const template = 'a'.repeat(40), initial = 'b'.repeat(40), merge = 'c'.repeat(40);
  const base = `${cfg.run_id}-base`, branch = `braidplane/${cfg.run_id}/proposal`;
  const file = `pipelines/${cfg.run_id}/${cfg.run_id}.pipeline.yaml`;
  const repo = 'https://github.com/fixture/test-pipelines.git';
  let version = 0, publishedName = '', savedSpec, marker, merged = false, mergeable = true, closed = false;
  let changeTemplate = false, templateReads = 0, pendingRefreshRead = false;
  const versionSpecs = [];
  const head = () => String(version + 1).repeat(40);
  const specHash = spec => createHash('sha256').update(JSON.stringify(spec)).digest('hex');
  const hash = () => specHash(versionSpecs[version]);
  const draft = () => ({draft_state: closed ? 'proposal_closed' : 'proposal_open', proposal_is_stale: false,
    proposal_state: closed ? 'closed' : 'open', proposal_provider: 'github', publication_mode: 'organization_service',
    provider_actor: cfg.actor, authority_source: 'fixture-installation', base_ref: `refs/heads/${base}`,
    proposal_id: '7', branch_name: branch, branch_head_sha: head(), last_proposed_draft_hash: 'd'.repeat(64), spec: savedSpec});
  const receipt = n => ({id: `proof-${n}`, candidate: {organization_slug: cfg.organization,
    project_slug: cfg.run_id, environment_slug: cfg.environment, provider_connection_slug: `${cfg.run_id}-git`, repository_id: '42',
    change_request_id: '7', head_sha: String(n + 1).repeat(40), target_ref: `refs/heads/${base}`, pipeline_path: file,
    spec_hash: `sha256:${specHash(versionSpecs[n])}`}, aggregate_verdict: 'unverified',
    claims: [{identity: {family: 'source_identity'}, description: cfg.token, verdict: 'unverified',
      required_evidence_level: 'static', actual_evidence_level: null}]});
  const deletions = [];
  const response = (data = {}, status = 200) => ({status, data});
  const request = async req => {
    const url = new URL(req.url), p = url.pathname, method = req.method;
    if (p.endsWith('/pipeline-spec:format')) return response({data: JSON.stringify(JSON.parse(req.data.data), null, 2)});
    if (method === 'DELETE') {
      deletions.push(p);
      return response({}, 204);
    }
    if (p === '/repos/fixture/test-pipelines') return response({id: 42, full_name: cfg.repository, private: true, default_branch: 'main'});
    if (p.endsWith('/branches/bp-934-e2e-base')) {
      templateReads++;
      return response({protected: false, commit: {sha: changeTemplate && templateReads > 1 ? 'd'.repeat(40) : template}});
    }
    if (p.includes('/git/commits/')) return response({tree: {sha: template}});
    if (p.endsWith('/git/trees')) { marker = req.data.tree[0].content; return response({sha: template}, 201); }
    if (p.endsWith('/git/commits')) return response({sha: initial}, 201);
    if (p.endsWith('/git/refs')) return response({}, req.data.sha === template ? 422 : 201);
    if (p.includes('/git/ref/heads/')) return response({object: {sha: initial}});
    if (p.includes('/contents/.braidplane-e2e/')) return response({content: Buffer.from(marker).toString('base64')});
    if (p.includes('/contents/pipelines/')) {
      if (method === 'GET') return response({sha: initial});
      mergeable = !JSON.parse(Buffer.from(req.data.content, 'base64').toString()).metadata.name.includes('conflicting');
      return response({commit: {sha: initial}}, 201);
    }
    const pull = () => ({number: 7, state: closed || merged ? 'closed' : 'open', user: {login: cfg.actor},
      head: {ref: branch, sha: head(), repo: {id: 42}}, base: {ref: base}, merged, mergeable, merge_commit_sha: merged ? merge : null});
    if (p.endsWith('/pulls')) return response([pull()]);
    if (p.endsWith('/pulls/7/merge')) {
      if (req.data.sha !== head()) return response({}, 409);
      if (!mergeable) return response({}, 405);
      merged = true; return response({merged: true, sha: merge});
    }
    if (p.endsWith('/pulls/7')) {
      if (method === 'PATCH') closed = req.data.state === 'closed';
      return response(pull());
    }
    if (p.includes('/check-runs')) {
      const n = url.pathname.includes(String(2).repeat(40)) ? 1 : 0;
      return response({check_runs: [{id: n + 10, name: 'Braidplane Proof', head_sha: String(n + 1).repeat(40),
        app: {id: 2}, conclusion: 'neutral', details_url: `${cfg.server_url}/proof/proof-${n}`, output: {summary: 'unverified'}}]});
    }
    if (p.endsWith('/proof-receipts')) return response({receipts: [receipt(version)], has_more: false});
    if (p.endsWith('/proof-receipts/proof-0')) return response({...receipt(0), aggregate_verdict: 'superseded'});
    if (p.endsWith('/draft') && method === 'PUT') { savedSpec = req.data.spec; return response({draft: draft()}); }
    if (p.endsWith('/draft')) {
      if (pendingRefreshRead) {
        pendingRefreshRead = false;
        return response({draft: {...draft(), branch_head_sha: '1'.repeat(40)}});
      }
      return response({draft: draft()});
    }
    if (p.endsWith('/draft:publish')) {
      if (req.headers.Authorization.endsWith(cfg.api_key)) return response({}, 403);
      if (req.data.expected_spec_hash === '0'.repeat(64)) return response({}, 409);
      if (publishedName && publishedName !== savedSpec.metadata.name) {
        version++;
        pendingRefreshRead = true;
      }
      versionSpecs[version] = {...structuredClone(savedSpec), metadata: {...savedSpec.metadata, slug: cfg.run_id}};
      publishedName = savedSpec.metadata.name; return response();
    }
    if (p.endsWith('/draft:refresh-proposal')) return response();
    if (p.endsWith('/pipelines:reconcile')) {
      if (req.data.expected_source.commit !== merge) return response({}, 409);
      return response({source: {...req.data.expected_source}, failures: []});
    }
    if (p.endsWith(`/pipelines/${cfg.run_id}`)) return response({slug: cfg.run_id, current_version_spec_hash: merged ? hash() : 'f'.repeat(64),
      last_applied_commit: merged ? merge : initial, source_path: file, source_ref: `refs/heads/${base}`});
    if (p.endsWith('/pipeline-backend')) return response({pipeline_backend: {repo}});
    if (p.endsWith(`/projects/${cfg.run_id}`) || p.endsWith('/projects')) return response({slug: cfg.run_id, name: `Synthetic ${cfg.run_id}`}, method === 'POST' ? 201 : 200);
    if (p.endsWith(`/data-sources/${cfg.run_id}-git`) || p.endsWith('/data-sources')) return response({slug: `${cfg.run_id}-git`, name: `Synthetic ${cfg.run_id}`, type: 'github', config: {repository: {id: '42', full_name: cfg.repository}}}, method === 'POST' ? 201 : 200);
    if (p.endsWith('/proof') || p.endsWith(`/environments/${cfg.environment}`)) return response();
    throw new Error('unhandled_fixture_request');
  };
  try {
    await run(cfg, request, response({id: 1, app_id: 2}));
    const saved = JSON.parse(fs.readFileSync(journal));
    assert.equal(saved.outcome, 'passed');
    assert.equal(saved.head, '2'.repeat(40));
    assert.equal(saved.merge_commit, merge);
    assert.deepEqual(saved.cleanup.failures, []);
    assert.equal(saved.assertions.length, 10);
    assert.deepEqual(saved.receipt_evidence.map(r => [r.receipt, r.aggregate_verdict]),
      [['proof-0', 'unverified'], ['proof-1', 'unverified'], ['proof-0', 'superseded']]);
    assert.equal(saved.receipt_evidence[0].clauses[0].required_evidence_level, 'static');
    assert.equal(JSON.stringify(saved).includes(cfg.token), false, 'receipt descriptions never enter the journal');
    assert.equal(fs.existsSync(`${journal}.tmp`), false);
    assert.throws(() => receiptEvidence({...receipt(0), aggregate_verdict: cfg.token}), /receipt_verdict_invalid/);
    assert.ok(deletions.includes(`/repos/fixture/test-pipelines/git/refs/heads/${base}`));
    assert.ok(deletions.includes(`/repos/fixture/test-pipelines/git/refs/heads/${branch}`));
    assert.equal(deletions.some(p => p.endsWith('/heads/main') || p.includes('bp-934-e2e-base')), false);
    fs.rmSync(journal);
    version = 0; publishedName = ''; merged = false; mergeable = true; closed = false;
    changeTemplate = true; templateReads = 0; deletions.length = 0;
    await assert.rejects(run(cfg, request, response({id: 1, app_id: 2})), /template_commit_changed/);
    const failed = JSON.parse(fs.readFileSync(journal));
    assert.equal(failed.outcome, 'failed');
    assert.deepEqual(failed.cleanup.failures, []);
    assert.ok(deletions.includes(`/repos/fixture/test-pipelines/git/refs/heads/${base}`));
    assert.equal(deletions.some(p => p.endsWith('/heads/main') || p.includes('bp-934-e2e-base')), false);
    fs.rmSync(journal);
    version = 0; publishedName = ''; merged = false; mergeable = true; closed = false;
    changeTemplate = false; templateReads = 0;
    const rejectedBackend = req => new URL(req.url).pathname.endsWith('/pipeline-backend')
      ? response({error: {code: 'EXECUTION_UNAVAILABLE', message: cfg.token}}, 503) : request(req);
    await assert.rejects(run(cfg, rejectedBackend, response({id: 1, app_id: 2})), /braidplane_put_503/);
    const denied = JSON.parse(fs.readFileSync(journal));
    assert.equal(denied.failure_operation.error_code, 'EXECUTION_UNAVAILABLE');
    assert.deepEqual(denied.cleanup.failures, []);
    assert.equal(JSON.stringify(denied).includes(cfg.token), false);
  } finally { fs.rmSync(journal, {force: true}); }
});
