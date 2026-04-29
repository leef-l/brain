import { useCallback, useEffect, useRef, useState } from "react";
import { SnakeGame, Direction } from "./game";
import GameOverOverlay from "./components/GameOverOverlay";
import TouchControls from "./components/TouchControls";
import "./App.css";

function App() {
  const gameRef = useRef<SnakeGame | null>(null);
  const [, setTick] = useState(0);
  const [grid, setGrid] = useState<number[][]>([]);
  const [state, setState] = useState(gameRef.current?.state ?? "IDLE");
  const [isNewBest, setIsNewBest] = useState(false);
  const prevHighScore = useRef(0);

  const syncState = useCallback(() => {
    const g = gameRef.current;
    if (!g) return;
    setGrid(g.getGrid());
    setState(g.state);
    // Detect new high score
    if (g.state === "GAME_OVER" && g.highScore > prevHighScore.current && g.score === g.highScore) {
      setIsNewBest(true);
    }
    prevHighScore.current = g.highScore;
  }, []);

  useEffect(() => {
    const game = new SnakeGame();
    gameRef.current = game;
    prevHighScore.current = game.highScore;

    const interval = setInterval(() => {
      syncState();
      setTick((t) => t + 1);
    }, 50);

    const handleKey = (e: KeyboardEvent) => {
      const g = gameRef.current;
      if (!g) return;

      // Ignore keyboard when game over overlay is showing and user can use Esc/Enter/Space
      // The overlay handles its own keyboard events
      if (g.state === "GAME_OVER") {
        if (e.key === "Escape" || e.key === "Enter" || e.key === " ") {
          // Let the overlay handle it - but also provide as fallback
          e.preventDefault();
          handleRestart();
        }
        return;
      }

      const keyMap: Record<string, Direction> = {
        ArrowUp: "UP",
        ArrowDown: "DOWN",
        ArrowLeft: "LEFT",
        ArrowRight: "RIGHT",
        w: "UP",
        s: "DOWN",
        a: "LEFT",
        d: "RIGHT",
        W: "UP",
        S: "DOWN",
        A: "LEFT",
        D: "RIGHT",
      };

      const dir = keyMap[e.key];
      if (dir) {
        e.preventDefault();
        if (g.state === "IDLE") {
          g.setDirection(dir);
          g.start();
        } else if (g.state === "PLAYING") {
          g.setDirection(dir);
        }
      }

      if (e.key === " " || e.key === "p" || e.key === "P") {
        e.preventDefault();
        if (g.state === "PLAYING") {
          g.pause();
        } else if (g.state === "PAUSED") {
          g.resume();
        } else if (g.state === "IDLE") {
          g.start();
        }
      }
    };

    window.addEventListener("keydown", handleKey);
    syncState();

    return () => {
      window.removeEventListener("keydown", handleKey);
      clearInterval(interval);
      game.destroy();
    };
  }, [syncState]);

  const handleStart = () => {
    const g = gameRef.current;
    if (!g) return;
    if (g.state === "GAME_OVER") g.reset();
    g.start();
    setIsNewBest(false);
    syncState();
  };

  const handleRestart = () => {
    const g = gameRef.current;
    if (!g) return;
    g.reset();
    g.start();
    setIsNewBest(false);
    syncState();
  };

  const handleDirection = (dir: Direction) => {
    const g = gameRef.current;
    if (!g) return;
    if (g.state === "IDLE" || g.state === "GAME_OVER") {
      g.setDirection(dir);
      g.start();
      setIsNewBest(false);
    } else if (g.state === "PLAYING") {
      g.setDirection(dir);
    }
    syncState();
  };

  const handleTouchAction = () => {
    const g = gameRef.current;
    if (!g) return;
    if (g.state === "PLAYING") {
      g.pause();
    } else if (g.state === "PAUSED") {
      g.resume();
    } else if (g.state === "IDLE" || g.state === "GAME_OVER") {
      if (g.state === "GAME_OVER") {
        g.reset();
        setIsNewBest(false);
      }
      g.start();
    }
    syncState();
  };

  return (
    <div className="app">
      <h1 className="title">🐍 Snake Game</h1>
      <div className="scoreboard">
        <span>
          Score: <strong>{gameRef.current?.score ?? 0}</strong>
        </span>
        <span>
          High Score: <strong>{gameRef.current?.highScore ?? 0}</strong>
        </span>
      </div>
      <div
        className="grid"
        style={{
          gridTemplateColumns: `repeat(${grid[0]?.length ?? 20}, 1fr)`,
        }}
      >
        {grid.flat().map((cell, i) => (
          <div
            key={i}
            className={`cell ${
              cell === 1
                ? "snake"
                : cell === 2
                  ? "food"
                  : cell === 3
                    ? "head"
                    : ""
            }`}
          />
        ))}
      </div>

      {/* Desktop buttons */}
      <div className="controls">
        {state === "IDLE" && (
          <button onClick={handleStart} className="btn btn-start">
            Start Game
          </button>
        )}
        {state === "PLAYING" && (
          <button
            onClick={() => {
              gameRef.current?.pause();
              syncState();
            }}
            className="btn btn-pause"
          >
            Pause
          </button>
        )}
        {state === "PAUSED" && (
          <button
            onClick={() => {
              gameRef.current?.resume();
              syncState();
            }}
            className="btn btn-resume"
          >
            Resume
          </button>
        )}
        {state === "GAME_OVER" && (
          <button onClick={handleStart} className="btn btn-restart">
            Play Again
          </button>
        )}
      </div>

      {/* Mobile touch controls */}
      <TouchControls
        onDirection={handleDirection}
        onAction={handleTouchAction}
      />

      <div className="instructions">
        <p>Use Arrow Keys or WASD to move • Space/P to pause</p>
        <p className="instructions-mobile">On mobile: swipe to move • tap to start/pause</p>
      </div>

      {/* Game Over Overlay */}
      <GameOverOverlay
        visible={state === "GAME_OVER"}
        score={gameRef.current?.score ?? 0}
        highScore={gameRef.current?.highScore ?? 0}
        isNewBest={isNewBest}
        onRestart={handleRestart}
      />
    </div>
  );
}

export default App;
