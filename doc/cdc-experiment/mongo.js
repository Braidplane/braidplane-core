// Loaded by run.py into the admin shell of the disposable replica set.
function equal(expected, actual) {
    if (EJSON.stringify(expected) !== EJSON.stringify(actual)) {
        throw new Error("Expected " + EJSON.stringify(expected) + ", got " + EJSON.stringify(actual));
    }
}
const sample = db.getSiblingDB("sample");
const majority = {w: "majority"};
const show = (label, value) => print(label + " " + EJSON.stringify(value));
const reader = () => new Mongo(
    "mongodb://capture:cdc-reader@127.0.0.1:27017/sample?directConnection=true&authSource=sample"
).getDB("sample");

show("version and replica set", {version: db.version(), set: db.hello().setName});
sample.createCollection("items");
sample.createRole({
    role: "captureOnly",
    privileges: [{resource: {db: "sample", collection: "items"}, actions: ["find", "changeStream"]}],
    roles: []
});
sample.createUser({user: "capture", pwd: "cdc-reader", roles: ["captureOnly"]});

const seed = sample.runCommand({
    insert: "items", documents: [{_id: 0, value: "seed"}], writeConcern: majority
});
const snapshotTime = seed.operationTime;
const capture = reader();
let denied;
try {
    capture.runCommand({delete: "items", deletes: [{q: {_id: 0}, limit: 1}]});
} catch (error) {
    denied = error;
}
equal(13, denied?.code);
show("capture write denied", 13);
sample.items.insertOne({_id: 1, value: "inserted"}, {writeConcern: majority});
const snapshot = capture.runCommand({
    find: "items", sort: {_id: 1}, readConcern: {level: "snapshot", atClusterTime: snapshotTime}
});
equal([{_id: 0, value: "seed"}], snapshot.cursor.firstBatch);
show("snapshot", {atClusterTime: snapshotTime, rows: snapshot.cursor.firstBatch});

function take(stream) {
    for (let attempt = 0; attempt < 10; attempt++) {
        const event = stream.tryNext();
        if (event) return event;
    }
    throw new Error("Timed out waiting for a change event");
}

function trace(label, event) {
    show(label, {
        operation: event.operationType, key: event.documentKey,
        at: event.clusterTime, token: event._id,
        document: event.fullDocument, changes: event.updateDescription
    });
}

const atSnapshot = {startAtOperationTime: snapshotTime, maxAwaitTimeMS: 500};
const first = capture.items.watch([], atSnapshot);
const boundary = take(first);
equal("insert", boundary.operationType);
equal(0, boundary.documentKey._id);
equal(snapshotTime, boundary.clusterTime);
trace("inclusive snapshot boundary", boundary);
const inserted = take(first);
equal("insert", inserted.operationType);
equal(1, inserted.documentKey._id);
trace("first read (unacknowledged)", inserted);
first.close();

sample.items.updateOne({_id: 1}, {$set: {value: "updated"}}, {writeConcern: majority});
sample.items.deleteOne({_id: 1}, {writeConcern: majority});
const replay = reader().items.watch([], atSnapshot);
equal(boundary._id, take(replay)._id);
const repeated = take(replay);
equal(inserted._id, repeated._id);
trace("fresh reader, old checkpoint", repeated);
const updated = take(replay);
const deleted = take(replay);
equal("update", updated.operationType);
equal(1, updated.documentKey._id);
equal("updated", updated.updateDescription.updatedFields.value);
equal("delete", deleted.operationType);
equal(1, deleted.documentKey._id);
trace("fresh reader, old checkpoint", updated);
trace("fresh reader, old checkpoint", deleted);
replay.close();

const resume = {resumeAfter: inserted._id, maxAwaitTimeMS: 500};
const restarted = reader().items.watch([], resume);
const resumedUpdate = take(restarted);
const resumedDelete = take(restarted);
equal(updated._id, resumedUpdate._id);
equal(deleted._id, resumedDelete._id);
trace("fresh reader after acknowledging insert", resumedUpdate);
trace("fresh reader after acknowledging insert", resumedDelete);
restarted.close();

// Force the oplog past the saved token using unobserved writes.
const oplog = db.getSiblingDB("local").getCollection("oplog.rs");
const data = "x".repeat(512 * 1024);
let oldest;
for (let i = 0; i < 256; i++) {
    sample.churn.insertOne({data}, {writeConcern: majority});
    if (i % 8 !== 7) continue;
    sleep(250);  // Let the fixture's background oplog truncation run.
    oldest = oplog.find().sort({$natural: 1}).limit(1).next().ts;
    if (oldest.compare(inserted.clusterTime) > 0) break;
}
show("oplog size", db.getReplicationInfo().logSizeMB);
show("oldest oplog entry after churn", oldest);
equal(true, oldest.compare(inserted.clusterTime) > 0);
let failure;
try {
    const expired = reader().items.watch([], resume);
    expired.tryNext();
    expired.close();
} catch (error) {
    failure = error;
}
equal(286, failure?.code);
show("expired resume token", {code: failure.code, message: failure.message});
