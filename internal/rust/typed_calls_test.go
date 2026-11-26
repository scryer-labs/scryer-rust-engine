package rust

import (
	"context"
	"testing"
)

func Test_should_only_link_unique_unconditional_explicit_local_receivers(t *testing.T) {
	root := rustFixture(t, map[string]string{"Cargo.toml": "[package]\nname='typed'\nedition='2021'\n", "src/lib.rs": `struct Worker;
impl Worker { fn step(&self) {} }
trait Task { fn task(&self); }
impl Task for Worker { fn task(&self) {} }
fn run(worker: &Worker) {
 worker.step();
 let another: Worker = Worker;
 another.step();
 <Worker as Task>::task(worker);
}
fn shadow(worker: &Worker) { let worker = unknown(); worker.step(); }
fn generic<Worker>(worker: Worker) { worker.step(); }
fn dynamic(worker: &dyn Task) { worker.task(); }
struct Conditional;
#[cfg(unix)] impl Conditional { fn step(&self) {} }
fn guarded(worker: &Conditional) { worker.step(); }
trait Other { fn task(&self); }
impl Other for Worker { fn task(&self) {} }
fn ambiguous(worker: &Worker) { worker.task(); }
`})
	result, err := Scan(context.Background(), root, []string{"call-graph"})
	if err != nil {
		t.Fatal(err)
	}
	graph := result["call-graph"].(*callGraph)
	names := map[string]string{}
	for _, symbol := range graph.Symbols {
		names[symbol.ID] = symbol.Name
	}
	linked := 0
	for _, call := range graph.Calls {
		switch names[call.CallerID] {
		case "run":
			if call.TargetID == "" {
				t.Errorf("explicit local receiver not linked: %+v", call)
			} else {
				linked++
			}
		case "shadow", "generic", "dynamic", "guarded", "ambiguous":
			if call.TargetID != "" {
				t.Errorf("uncertain dispatch falsely linked: %+v", call)
			}
		}
	}
	if linked != 3 {
		t.Errorf("linked %d, want three explicit source candidates", linked)
	}
}
