"""Exercise the packaged v1 ABI with a temporary source project."""
import ctypes
import json
import sys
import tempfile
from pathlib import Path

package = Path(sys.argv[1]).resolve()
manifest = json.loads((package / "engine.json").read_text())
library = ctypes.CDLL(str(package / manifest["library"]))


class View(ctypes.Structure):
    _fields_ = [("data", ctypes.c_char_p), ("size", ctypes.c_size_t)]


class Buffer(ctypes.Structure):
    _fields_ = [("data", ctypes.c_void_p), ("size", ctypes.c_size_t)]


class Result(ctypes.Structure):
    _fields_ = [("status", ctypes.c_int32), ("response", Buffer)]


library.execute_v1.argtypes = [View]
library.execute_v1.restype = Result
library.cancel_v1.argtypes = [View]
library.cancel_v1.restype = ctypes.c_bool
library.release_buffer_v1.argtypes = [Buffer]
library.release_buffer_v1.restype = None
with tempfile.TemporaryDirectory() as directory:
    root = Path(directory).resolve()
    (root / "main.rs").write_text("fn main() {}\n")
    request = json.dumps({
        "protocolVersion": 1, "requestId": "package-check", "stack": "rust",
        "operation": "scan", "scan": {"repository": str(root), "facts": ["files"], "static": True},
    }).encode()
    result = library.execute_v1(View(request, len(request)))
    try:
        response = json.loads(ctypes.string_at(result.response.data, result.response.size))
        assert result.status == 0 and response["error"] is None, response
        assert any(item["path"] == "main.rs" for item in response["report"]["facts"]["files"]["files"])
    finally:
        library.release_buffer_v1(result.response)


# Shared analysis operations must also survive packaging and ABI transport.
assert {"scan", "impact", "coverage"} <= set(manifest["capabilities"]["operations"])
def execute(request):
    data = json.dumps({"protocolVersion": 1, "requestId": "analysis-package-"+request["operation"], "stack": 'rust', **request}).encode()
    result = library.execute_v1(View(data, len(data)))
    try:
        response = json.loads(ctypes.string_at(result.response.data, result.response.size))
        assert result.status == 0 and response["error"] is None, response
        return response["report"]
    finally:
        library.release_buffer_v1(result.response)

with tempfile.TemporaryDirectory() as directory:
    root = Path(directory).resolve()
    before, after = root / "before", root / "after"
    before.mkdir(); after.mkdir()
    (before / "value.rs").write_text('pub fn value() -> i32 { 1 }\n')
    (after / "value.rs").write_text('pub fn value() -> i32 { 2 }\n')
    report = execute({"operation":"impact", "impact":{"before":"a"*40, "after":"b"*40, "beforeRoot":str(before), "afterRoot":str(after), "resolve":False}})
    left, right = report["before"]["symbols"][0], report["after"]["symbols"][0]
    assert left["key"] == right["key"] and left["fingerprint"] != right["fingerprint"], report
    source = root / "value.rs"
    source.write_text('pub fn value() -> i32 { 1 }\n')
    artifact = root / "coverage.json"
    summary = {name:{"count":1,"covered":1} for name in ["lines","functions","regions"]}
    artifact.write_text(json.dumps({"type":"llvm.coverage.json.export","version":"2.0.1","data":[{"files":[{"filename":str(source),"summary":summary,"segments":[[1,1,1,True,True,False],[1,25,0,False,False,False]]}]}]}))
    coverage = execute({"operation":"coverage","coverage":{"repository":str(root),"artifact":str(artifact)}})
    assert coverage["status"] == "collected" and coverage["coverage"]["files"], coverage


# Exercise closure correspondence through the real packaged ABI on both hosts.
with tempfile.TemporaryDirectory() as directory:
    root = Path(directory).resolve() / "space 项目"
    before, after = root / "before", root / "after"
    before.mkdir(parents=True)
    after.mkdir()
    original = "fn target() {}\nfn run() { let callback = || { target(); }; }\n"
    (before / "source.rs").write_bytes(original.encode())
    (after / "source.rs").write_bytes(("// shifted\r\n" + original.replace("\n", "\r\n").replace("target();", "target(); target();")).encode())
    report = execute({"operation": "impact", "impact": {"before": "a" * 40, "after": "b" * 40, "beforeRoot": str(before), "afterRoot": str(after), "resolve": False}})
    left = [item for item in report["before"]["symbols"] if "::closure[" in item["signature"]]
    right = [item for item in report["after"]["symbols"] if "::closure[" in item["signature"]]
    assert len(left) == len(right) == 1, report
    assert left[0]["key"] == right[0]["key"] and left[0]["fingerprint"] != right[0]["fingerprint"], report
    calls = [item for item in report["after"]["calls"] if item["callerSymbolId"] == right[0]["id"]]
    assert len(calls) == 2 and all(item["kind"] == "source-candidate" for item in calls), report
