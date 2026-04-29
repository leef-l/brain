import { useState } from "react";
import Hello from "./Hello";

function App() {
  const [count, setCount] = useState(0);

  return (
    <div className="app">
      <Hello name="Brain" />
      <div className="card">
        <button onClick={() => setCount((c) => c + 1)}>
          Count is {count}
        </button>
        <p>
          Edit <code>src/App.tsx</code> and save to test HMR
        </p>
      </div>
    </div>
  );
}

export default App;
