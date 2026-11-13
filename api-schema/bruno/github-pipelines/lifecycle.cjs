const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {randomUUID, createHash} = require('node:crypto');

const sha = /^[0-9a-f]{40}$/;
const slug = /^[a-z][a-z0-9-]*$/;

function config(env) {
  const cfg = JSON.parse(env('BRAIDPLANE_GITHUB_E2E_CONFIG') || '{}');
  assert.match(cfg.run_id || '', /^bp-934-\d{8}-[0-9a-f]{12}$/, 'unique_run_identity_required');
  assert.match(cfg.repository || '', /^[A-Za-z0-9-]+\/(test-pipelines|proof-provider-e2e)$/, 'isolated_fixture_repository_required');
  assert.match(cfg.base_branch || '', /^bp-934-e2e-base$/, 'dedicated_template_base_required');
  assert.match(cfg.template_commit || '', sha, 'pinned_template_commit_required');
  for (const key of ['organization', 'environment']) assert.match(cfg[key] || '', slug, 'explicit_target_required');
  for (const key of ['installation_id', 'app_id']) assert.match(String(cfg[key] || ''), /^[1-9][0-9]*$/, 'installed_identity_required');
  assert.match(cfg.actor || '', /^[A-Za-z0-9-]+\[bot\]$/, 'installation_bot_actor_required');
  const server = new URL(cfg.server_url);
  assert.equal(server.protocol, 'https:', 'https_required');
  assert.equal(server.username + server.password + server.search + server.hash, '', 'bare_server_origin_required');
  assert.equal(server.pathname, '/', 'bare_server_origin_required');
  cfg.server_url = server.origin;
  cfg.cleanup_only = env('BRAIDPLANE_GITHUB_E2E_CLEANUP') === '--cleanup';
  cfg.token = env('BRAIDPLANE_GITHUB_E2E_TOKEN');
  cfg.app_jwt = env('BRAIDPLANE_GITHUB_E2E_APP_JWT');
  cfg.pat = env('BRAIDPLANE_GITHUB_E2E_PAT');
  cfg.api_key = env('BRAIDPLANE_GITHUB_E2E_API_KEY');
  for (const key of ['token', 'app_jwt', 'pat', 'api_key']) assert.ok(cfg[key], 'task_owned_credential_required');
  return cfg;
}

function fixture(runID) {
  return {
    metadata: {slug: runID, name: `Synthetic ${runID}`},
    steps: [{slug: 'payload', kind: 'input', input: {output_contract: {
      name: 'synthetic-payload', representation: 'json', root_shape: 'object',
      fields: [{name: 'message', type: 'string', required: true}],
    }}}, {slug: 'echo', kind: 'transform', transform: {
      inputs: {input: 'payload'}, sql: 'SELECT message FROM input', output_kind: 'rows',
    }}],
  };
}

function assertReceipt(receipt, cfg, state, head, specHash) {
  const c = receipt.candidate;
  assert.equal(c.organization_slug, cfg.organization, 'receipt_tenant_mismatch');
  assert.equal(c.project_slug, cfg.run_id, 'receipt_project_mismatch');
  assert.equal(c.environment_slug, cfg.environment, 'receipt_environment_mismatch');
  assert.equal(c.repository_id, String(state.repository_id), 'receipt_repository_mismatch');
  assert.equal(c.change_request_id, String(state.pull), 'receipt_pr_mismatch');
  assert.equal(c.head_sha, head, 'receipt_head_mismatch');
  assert.equal(c.target_ref, `refs/heads/${state.base}`, 'receipt_ref_mismatch');
  assert.equal(c.pipeline_path, state.pipeline_path, 'receipt_path_mismatch');
  assert.equal(c.spec_hash, `sha256:${specHash}`, 'receipt_spec_mismatch');
  assert.equal(c.provider_connection_slug, `${cfg.run_id}-git`, 'receipt_connection_mismatch');
  assert.ok(receipt.claims.length, 'clause_level_receipt_required');
}

function receiptEvidence(receipt) {
  const verdicts = ['passed', 'failed', 'unverified', 'inconclusive', 'stale', 'superseded'];
  const levels = ['static', 'cached_observed', 'live_read_only', 'bounded_execution'];
  const digest = value => `sha256:${createHash('sha256').update(JSON.stringify(value)).digest('hex')}`;
  assert.match(receipt.id, /^proof-[a-z0-9-]{1,94}$/, 'receipt_identity_invalid');
  assert.match(receipt.candidate.head_sha, sha, 'receipt_head_invalid');
  assert.match(receipt.candidate.spec_hash, /^sha256:[0-9a-f]{64}$/, 'receipt_spec_invalid');
  assert.ok(verdicts.includes(receipt.aggregate_verdict), 'receipt_verdict_invalid');
  const clauses = receipt.claims.map(claim => {
    assert.ok(verdicts.includes(claim.verdict) && claim.verdict !== 'superseded', 'clause_verdict_invalid');
    assert.ok(levels.includes(claim.required_evidence_level), 'clause_level_invalid');
    assert.ok(claim.actual_evidence_level == null || levels.includes(claim.actual_evidence_level), 'clause_level_invalid');
    return {identity_digest: digest(claim.identity), verdict: claim.verdict,
      required_evidence_level: claim.required_evidence_level, actual_evidence_level: claim.actual_evidence_level ?? null};
  });
  return {receipt: receipt.id, head: receipt.candidate.head_sha, spec_hash: receipt.candidate.spec_hash,
    aggregate_verdict: receipt.aggregate_verdict, digest: digest(receipt), clauses};
}

async function run(cfg, request, installation) {
  assert.equal(installation.status, 200, 'installation_credential_required');
  assert.equal(String(installation.data.id), String(cfg.installation_id), 'installation_mismatch');
  assert.equal(String(installation.data.app_id), String(cfg.app_id), 'app_mismatch');
  const journal = path.resolve(__dirname, '../../../.worktree/bp-934', `${cfg.run_id}.json`);
  assert.ok(!cfg.cleanup_only || fs.existsSync(journal), 'cleanup_ownership_journal_required');
  fs.mkdirSync(path.dirname(journal), {recursive: true, mode: 0o700});
  const target = {server_url: cfg.server_url, organization: cfg.organization, environment: cfg.environment, repository: cfg.repository};
  let state = fs.existsSync(journal) ? JSON.parse(fs.readFileSync(journal, 'utf8')) : {
    run_id: cfg.run_id, target, base: `${cfg.run_id}-base`,
    attempt_id: randomUUID(),
    pipeline_dir: `pipelines/${cfg.run_id}`, pipeline_path: `pipelines/${cfg.run_id}/${cfg.run_id}.pipeline.yaml`,
    started_at: new Date().toISOString(), assertions: [], branches: [], outcome: 'running',
  };
  assert.equal(state.run_id, cfg.run_id, 'journal_run_mismatch');
  assert.deepEqual(state.target, target, 'journal_target_mismatch');
  const save = () => {
    const temporary = `${journal}.tmp`;
    try {
      fs.writeFileSync(temporary, JSON.stringify(state, null, 2) + '\n', {mode: 0o600});
      fs.renameSync(temporary, journal);
    } finally {
      fs.rmSync(temporary, {force: true});
    }
  };
  save();
  const ghRoot = `https://api.github.com/repos/${cfg.repository}`;
  const orgRoot = `${cfg.server_url}/api/organizations/${cfg.organization}`;
  const projectRoot = `${orgRoot}/projects/${cfg.run_id}`;
  const pipelineRoot = `${projectRoot}/pipelines/${cfg.run_id}`;
  const dataSource = `${cfg.run_id}-git`;
  const marker = `.braidplane-e2e/${cfg.run_id}.json`;
  let lastCall;
  async function call(root, method, endpoint, data, statuses = [200], credential = cfg.pat) {
    const github = root.startsWith('https://api.github.com/');
    lastCall = {provider: github ? 'github' : 'braidplane', method, endpoint};
    let response;
    try {
      response = await request({method, url: root + endpoint, data, timeout: 30000,
        maxRedirects: 0, headers: {Authorization: `Bearer ${github ? cfg.token : credential}`,
          'Content-Type': 'application/json', Accept: 'application/vnd.github+json'}});
    } catch {
      throw new Error('provider_or_network_unavailable');
    }
    lastCall.status = Number.isInteger(response.status) ? response.status : 'missing';
    if (!statuses.includes(response.status)) {
      const code = response.data?.error?.code;
      if (typeof code === 'string' && /^[A-Z][A-Z0-9_]{0,79}$/.test(code)) lastCall.error_code = code;
      throw new Error(`${github ? 'github' : 'braidplane'}_${method.toLowerCase()}_${response.status}`);
    }
    return response.data;
  }
  const gh = (method, endpoint, data, statuses) => call(ghRoot, method, endpoint, data, statuses);
  const api = (method, endpoint, data, statuses, credential) => call(orgRoot, method, endpoint, data, statuses, credential);
  async function poll(read, accept, label) {
    const end = Date.now() + 120000;
    do {
      const value = await read();
      if (accept(value)) return value;
      await new Promise(resolve => setTimeout(resolve, 2000));
    } while (Date.now() < end);
    throw new Error(`${label}_timeout`);
  }
  const record = label => { state.assertions.push({name: label, at: new Date().toISOString()}); save(); };
  async function putFile(branch, file, content) {
    const old = await gh('GET', `/contents/${file}?ref=${encodeURIComponent(branch)}`, undefined, [200, 404]);
    const result = await gh('PUT', `/contents/${file}`, {
      branch, message: `BP-934 synthetic fixture ${cfg.run_id}`, content: Buffer.from(content).toString('base64'),
      ...(old.sha ? {sha: old.sha} : {}),
    }, [200, 201]);
    assert.match(result.commit.sha, sha, 'exact_commit_required');
    return result.commit.sha;
  }
  async function branchOwned(branch, head) {
    assert.ok(branch === state.base || branch.includes(cfg.run_id), 'unowned_branch');
    if (head !== undefined) assert.match(head, sha, 'ownership_head_invalid');
    const value = await gh('GET', `/contents/${marker}?ref=${encodeURIComponent(head ?? branch)}`, undefined, [200, 404]);
    assert.ok(value.content, 'branch_ownership_marker_missing');
    assert.deepEqual(JSON.parse(Buffer.from(value.content, 'base64').toString()),
      {run_id: cfg.run_id, attempt_id: state.attempt_id}, 'branch_ownership_mismatch');
  }
  async function cleanup() {
    const failures = [];
    const attempt = async (label, action) => {
      try { await action(); } catch { failures.push(label); }
    };
    if (!state.base_created && state.branches.includes(state.base)) {
      // A response lost after ref creation is recoverable; a colliding run's
      // distinct first-commit marker never grants ownership of its resources.
      await attempt('run_ownership', async () => {
        const ref = await gh('GET', `/git/ref/heads/${state.base}`, undefined, [200, 404]);
        if (ref.object) { await branchOwned(state.base); state.base_created = true; save(); }
      });
    }
    if (!state.base_created) {
      if (failures.length) state.outcome = 'failed';
      state.cleanup = {at: new Date().toISOString(), failures}; save();
      assert.equal(failures.length, 0, 'scoped_cleanup_incomplete');
      return;
    }
    // Discover proposals even if creation succeeded but its response was lost.
    await attempt('proposals', async () => {
      const pulls = await gh('GET', `/pulls?state=all&base=${state.base}&per_page=100`);
      for (const pull of pulls) await attempt('proposal', async () => {
        assert.equal(pull.base.ref, state.base, 'cleanup_base_mismatch');
        assert.equal(pull.head.repo.id, state.repository_id, 'cleanup_repository_mismatch');
        // Closed PR heads retain ownership evidence after their branch is deleted.
        await branchOwned(pull.head.ref, pull.state === 'closed' ? pull.head.sha : undefined);
        if (!state.branches.includes(pull.head.ref)) state.branches.push(pull.head.ref);
        save();
        if (pull.state === 'open') await gh('PATCH', `/pulls/${pull.number}`, {state: 'closed'});
      });
    });
    await attempt('project', async () => {
      const project = await api('GET', `/projects/${cfg.run_id}`, undefined, [200, 404]);
      if (project.slug) {
        assert.equal(project.name, `Synthetic ${cfg.run_id}`, 'project_ownership_mismatch');
        await api('DELETE', `/projects/${cfg.run_id}?cleanup_remote=false`, undefined, [204]);
      }
    });
    await attempt('data_source', async () => {
      const ds = await api('GET', `/data-sources/${dataSource}`, undefined, [200, 404]);
      if (ds.slug) {
        assert.equal(ds.name, `Synthetic ${cfg.run_id}`, 'data_source_ownership_mismatch');
        assert.equal(ds.type, 'github', 'data_source_type_mismatch');
        assert.equal(String(ds.config.repository.id), String(state.repository_id), 'data_source_repository_mismatch');
        assert.equal(ds.config.repository.full_name, cfg.repository, 'data_source_repository_name_mismatch');
        await api('DELETE', `/data-sources/${dataSource}`, undefined, [204]);
      }
    });
    for (const branch of [...new Set(state.branches)].reverse()) await attempt('branch', async () => {
      const ref = await gh('GET', `/git/ref/heads/${branch}`, undefined, [200, 404]);
      if (!ref.object) return;
      await branchOwned(branch);
      await gh('DELETE', `/git/refs/heads/${branch}`, undefined, [204]);
    });
    if (failures.length) state.outcome = 'failed';
    state.cleanup = {at: new Date().toISOString(), failures}; save();
    assert.equal(failures.length, 0, 'scoped_cleanup_incomplete');
  }
  if (cfg.cleanup_only) { await cleanup(); return; }
  assert.equal(state.branches.length, 0, 'interrupted_run_requires_cleanup_first');
  const repository = await gh('GET', '');
  assert.equal(repository.full_name.toLowerCase(), cfg.repository.toLowerCase(), 'fixture_repository_mismatch');
  assert.equal(repository.private, true, 'private_fixture_required');
  assert.notEqual(repository.default_branch, cfg.base_branch, 'normal_default_branch_forbidden');
  state.repository_id = repository.id; save();
  try {
    const template = await gh('GET', `/branches/${cfg.base_branch}`);
    assert.ok(template.commit.sha === cfg.template_commit, 'template_commit_mismatch');
    state.template_commit = cfg.template_commit; save();
    // Journal before mutation, so a lost response remains recoverable.
    const spec = fixture(cfg.run_id);
    const templateCommit = await gh('GET', `/git/commits/${template.commit.sha}`);
    const tree = await gh('POST', '/git/trees', {base_tree: templateCommit.tree.sha, tree: [
      {path: marker, mode: '100644', type: 'blob', content: JSON.stringify({run_id: cfg.run_id, attempt_id: state.attempt_id})},
      {path: state.pipeline_path, mode: '100644', type: 'blob', content: JSON.stringify(spec)},
    ]}, [201]);
    const commit = await gh('POST', '/git/commits', {message: `BP-934 synthetic fixture ${cfg.run_id}`,
      tree: tree.sha, parents: [template.commit.sha]}, [201]);
    assert.match(commit.sha, sha, 'exact_fixture_commit_required');
    state.initial_commit = commit.sha; state.branches.push(state.base); save();
    await gh('POST', '/git/refs', {ref: `refs/heads/${state.base}`, sha: commit.sha}, [201]);
    state.base_created = true; save();
    await gh('POST', '/git/refs', {ref: `refs/heads/${state.base}`, sha: template.commit.sha}, [422]);
    assert.equal((await gh('GET', `/git/ref/heads/${state.base}`)).object.sha, state.initial_commit);
    record('naming_conflict_preserves_exact_base');
    const project = await api('POST', '/projects', {slug: cfg.run_id, name: `Synthetic ${cfg.run_id}`}, [201]);
    assert.equal(project.slug, cfg.run_id, 'project_slug_substitution');
    const ds = await api('POST', '/data-sources', {slug: dataSource, name: `Synthetic ${cfg.run_id}`, type: 'github',
      config: {repository_id: String(repository.id)}, read_only: false}, [201]);
    assert.equal(ds.slug, dataSource, 'data_source_slug_substitution');
    await api('PUT', `/projects/${cfg.run_id}/environments/${cfg.environment}`, {is_default: true});
    const configuredProject = await api('PUT', `/projects/${cfg.run_id}/pipeline-backend`, {data_source_slug: dataSource,
      tracked_branch: state.base, pipeline_dir: state.pipeline_dir, mode: 'adopt_existing'});
    await api('PUT', `/data-sources/${dataSource}/proof`, {enabled: true, publish_checks: true,
      expected_repository_id: String(repository.id), targets: [{project_slug: cfg.run_id,
        environment_slug: cfg.environment, target_ref: `refs/heads/${state.base}`, pipeline_path: state.pipeline_path}]});
    const pipeline = await call(pipelineRoot, 'GET', '');
    state.initial_hash = pipeline.current_version_spec_hash;
    assert.match(state.initial_hash, /^[0-9a-f]{64}$/, 'canonical_spec_hash_required');
    assert.equal(pipeline.last_applied_commit, state.initial_commit, 'initial_provenance_mismatch'); save();
    const draftSpec = {metadata: {name: `Synthetic ${cfg.run_id} proposal`}, steps: spec.steps};
    const versionHash = async draft => {
      const formatted = await api('POST', '/pipeline-spec:format', {format: 'json',
        data: JSON.stringify({...draft, metadata: {...draft.metadata, slug: cfg.run_id}})});
      return createHash('sha256').update(JSON.stringify(JSON.parse(formatted.data))).digest('hex');
    };
    const firstSpecHash = await versionHash(draftSpec);
    await call(pipelineRoot, 'PUT', '/draft', {spec: draftSpec, expected_spec_hash: state.initial_hash});
    await call(pipelineRoot, 'POST', '/draft:publish', {expected_spec_hash: state.initial_hash}, [403], cfg.api_key);
    record('api_key_cannot_publish_user_proposal');
    await call(pipelineRoot, 'POST', '/draft:publish', {expected_spec_hash: '0'.repeat(64)}, [409]);
    assert.equal((await call(pipelineRoot, 'GET', '/draft')).draft.spec.metadata.name, draftSpec.metadata.name);
    record('stale_version_failure_preserves_draft');
    const publish = async previousHead => {
      await call(pipelineRoot, 'POST', '/draft:publish', {expected_spec_hash: state.initial_hash});
      return poll(async () => (await call(pipelineRoot, 'GET', '/draft')).draft,
        draft => draft.draft_state === 'proposal_open' && !draft.proposal_is_stale && draft.branch_head_sha !== previousHead, 'proposal');
    };
    let draft = await publish();
    state.pull = Number(draft.proposal_id); state.proposal_branch = draft.branch_name;
    state.branches.push(draft.branch_name); state.head = draft.branch_head_sha; save();
    assert.equal(draft.proposal_provider, 'github');
    assert.equal(draft.publication_mode, 'organization_service');
    assert.equal(draft.provider_actor, cfg.actor, 'publishing_actor_mismatch');
    assert.ok(draft.authority_source, 'provider_authority_required');
    assert.equal(draft.base_ref, `refs/heads/${state.base}`);
    const pull = await gh('GET', `/pulls/${state.pull}`);
    assert.equal(pull.head.sha, state.head); assert.equal(pull.head.ref, state.proposal_branch);
    assert.equal(pull.head.repo.id, repository.id); assert.equal(pull.base.ref, state.base);
    assert.equal(pull.user.login, cfg.actor, 'provider_publishing_actor_mismatch');
    record('installed_identity_proposal_and_exact_provenance');
    const receiptsRoot = `${projectRoot}/environments/${cfg.environment}/proof-receipts`;
    const receiptFor = head => poll(async () => (await call(receiptsRoot, 'GET', '?limit=100')).receipts,
      list => list.some(r => r.candidate.head_sha === head && r.candidate.change_request_id === String(state.pull)), 'receipt')
      .then(list => list.find(r => r.candidate.head_sha === head && r.candidate.change_request_id === String(state.pull)));
    const firstReceipt = await receiptFor(state.head);
    assertReceipt(firstReceipt, cfg, state, state.head, firstSpecHash);
    state.receipt_evidence = [receiptEvidence(firstReceipt)]; save();
    const checkFor = receipt => poll(async () => (await gh('GET', `/commits/${receipt.candidate.head_sha}/check-runs?check_name=Braidplane%20Proof&filter=all&per_page=100`)).check_runs,
      list => list.some(c => c.name === 'Braidplane Proof' && c.details_url?.endsWith(`/${receipt.id}`)), 'proof_check')
      .then(list => {
        const checks = list.filter(c => c.details_url?.endsWith(`/${receipt.id}`));
        assert.equal(checks.length, 1, 'duplicate_proof_check');
        const check = checks[0]; assert.equal(check.head_sha, receipt.candidate.head_sha);
        assert.equal(check.app.id, Number(cfg.app_id), 'check_app_mismatch');
        assert.equal(check.conclusion, 'neutral', 'informational_check_required');
        const url = new URL(check.details_url); assert.equal(url.origin, cfg.server_url);
        assert.equal(url.search + url.hash, '', 'protected_link_must_be_bare');
        const text = JSON.stringify(check.output);
        for (const secret of [cfg.token, cfg.app_jwt, cfg.pat, cfg.api_key]) assert.equal(text.includes(secret), false, 'provider_secret_exposure');
        assert.equal((check.output.annotations_count || 0), 0, 'unexpected_provider_annotations');
        return check;
      });
    state.receipt = firstReceipt.id; state.check = (await checkFor(firstReceipt)).id; save();
    draft = await publish(); assert.equal(Number(draft.proposal_id), state.pull);
    assert.equal(draft.branch_head_sha, state.head); assert.equal((await checkFor(firstReceipt)).id, state.check);
    record('proposal_retry_preserves_pr_head_and_check');
    // A second authored version must create a new exact-head receipt.
    draftSpec.metadata.name += ' refreshed';
    await call(pipelineRoot, 'PUT', '/draft', {spec: draftSpec, expected_spec_hash: state.initial_hash});
    draft = await publish(state.head);
    assert.notEqual(draft.branch_head_sha, state.head, 'new_draft_requires_new_head');
    const oldHead = state.head; state.head = draft.branch_head_sha; state.spec_hash = await versionHash(draftSpec); save();
    const freshReceipt = await receiptFor(state.head);
    assertReceipt(freshReceipt, cfg, state, state.head, state.spec_hash);
    state.receipt_evidence.push(receiptEvidence(freshReceipt)); save();
    assert.notEqual(freshReceipt.id, firstReceipt.id, 'receipt_cannot_transfer_to_new_head');
    await checkFor(freshReceipt);
    const oldReceipt = await call(receiptsRoot, 'GET', `/${firstReceipt.id}`);
    assert.equal(oldReceipt.id, firstReceipt.id, 'superseded_receipt_identity_mismatch');
    assertReceipt(oldReceipt, cfg, state, oldHead, firstReceipt.candidate.spec_hash.slice(7));
    assert.equal(oldReceipt.aggregate_verdict, 'superseded');
    state.receipt_evidence.push(receiptEvidence(oldReceipt)); save();
    await gh('PUT', `/pulls/${state.pull}/merge`, {sha: oldHead, merge_method: 'merge'}, [409]);
    record('stale_head_cannot_merge_or_inherit_receipt');
    await gh('PATCH', `/pulls/${state.pull}`, {state: 'closed'});
    await call(pipelineRoot, 'POST', '/draft:refresh-proposal');
    await poll(async () => (await call(pipelineRoot, 'GET', '/draft')).draft,
      value => value.proposal_state === 'closed', 'cancel');
    assert.equal((await call(pipelineRoot, 'GET', '')).current_version_spec_hash, state.initial_hash);
    await gh('PATCH', `/pulls/${state.pull}`, {state: 'open'});
    record('cancellation_does_not_materialize_proposal');
    // Conflict only on this run's base and path, then restore the base file.
    const conflict = {...spec, metadata: {...spec.metadata, name: `${spec.metadata.name} conflicting base`}};
    await putFile(state.base, state.pipeline_path, JSON.stringify(conflict));
    await poll(() => gh('GET', `/pulls/${state.pull}`), value => value.mergeable === false, 'merge_conflict');
    await gh('PUT', `/pulls/${state.pull}/merge`, {sha: state.head, merge_method: 'merge'}, [405, 409]);
    assert.equal((await call(pipelineRoot, 'GET', '')).current_version_spec_hash, state.initial_hash);
    record('merge_conflict_does_not_materialize_proposal');
    await putFile(state.base, state.pipeline_path, JSON.stringify(spec));
    await poll(() => gh('GET', `/pulls/${state.pull}`), value => value.mergeable === true, 'merge_ready');
    const merged = await gh('PUT', `/pulls/${state.pull}/merge`, {sha: state.head, merge_method: 'merge'});
    assert.equal(merged.merged, true); assert.match(merged.sha, sha); state.merge_commit = merged.sha; save();
    const authoritative = await gh('GET', `/pulls/${state.pull}`);
    assert.equal(authoritative.merged, true); assert.equal(authoritative.merge_commit_sha, state.merge_commit);
    assert.notEqual(state.merge_commit, state.head, 'merge_and_source_identity_are_distinct');
    const expected = {data_source_slug: dataSource, repo: configuredProject.pipeline_backend.repo,
      ref: `refs/heads/${state.base}`, commit: state.merge_commit};
    await call(projectRoot, 'POST', '/pipelines:reconcile', {pipeline_dir: state.pipeline_dir,
      pipeline_slugs: [cfg.run_id], expected_source: {...expected, commit: state.head}}, [409]);
    const reconcile = await call(projectRoot, 'POST', '/pipelines:reconcile', {pipeline_dir: state.pipeline_dir,
      pipeline_slugs: [cfg.run_id], expected_source: expected});
    assert.equal(reconcile.source.commit, state.merge_commit); assert.equal(reconcile.failures.length, 0);
    const materialized = await call(pipelineRoot, 'GET', '');
    assert.equal(reconcile.source.repo, expected.repo); assert.equal(reconcile.source.ref, expected.ref);
    assert.equal(reconcile.source.data_source_slug, dataSource); assert.equal(materialized.source_ref, expected.ref);
    assert.equal(materialized.source_path, state.pipeline_path); assert.equal(materialized.last_applied_commit, state.merge_commit);
    assert.equal(materialized.current_version_spec_hash, state.spec_hash);
    record('exact_merge_commit_reconciliation_and_materialized_provenance');
    assert.ok((await gh('GET', `/branches/${cfg.base_branch}`)).commit.sha === cfg.template_commit, 'template_commit_changed');
    record('shared_pinned_template_unchanged');
    state.outcome = 'passed';
  } catch (error) {
    state.outcome = 'failed';
    state.failure_operation = lastCall;
    // Store classification only. Assertion values and provider errors can contain secrets.
    const classification = error.message.split('\n', 1)[0];
    state.failure = /^[a-z_]+(?:_\d{3})?$/.test(classification) ? classification : 'assertion_failed';
    throw new Error(state.failure);
  } finally {
    state.finished_at = new Date().toISOString(); save();
    await cleanup();
  }
}

module.exports = {config, fixture, assertReceipt, receiptEvidence, run};
