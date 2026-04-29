import { useEffect } from "react";
import "./GameOverOverlay.css";

interface GameOverOverlayProps {
  visible: boolean;
  score: number;
  highScore: number;
  isNewBest: boolean;
  onRestart: () => void;
}

export default function GameOverOverlay({
  visible,
  score,
  highScore,
  isNewBest,
  onRestart,
}: GameOverOverlayProps) {
  // Trap focus and handle Escape key
  useEffect(() => {
    if (!visible) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape" || e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onRestart();
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [visible, onRestart]);

  if (!visible) return null;

  return (
    <div
      className="gameover-overlay"
      role="dialog"
      aria-modal="true"
      aria-label="Game over"
    >
      <div className="gameover-backdrop" onClick={onRestart} />
      <div className="gameover-panel">
        {/* Confetti pieces */}
        <div className="gameover-confetti" aria-hidden="true">
          {Array.from({ length: 16 }).map((_, i) => (
            <span
              key={i}
              className="gameover-confetti-piece"
              style={{
                ["--x" as string]: `${Math.random() * 100}%`,
                ["--delay" as string]: `${Math.random() * 3}s`,
                ["--hue" as string]: Math.floor(Math.random() * 360),
                ["--drift" as string]: `${(Math.random() - 0.5) * 200}px`,
              }}
            />
          ))}
        </div>

        <h2 className="gameover-heading">
          {isNewBest ? "🏆 New High Score!" : "🐍 Game Over"}
        </h2>

        {isNewBest && (
          <p className="gameover-new-best-badge">Personal Best!</p>
        )}

        <div className="gameover-stats">
          <div className="gameover-stat">
            <span className="gameover-stat-label">Final Score</span>
            <span className="gameover-stat-value gameover-stat-value--score">
              {score}
            </span>
          </div>
          <div className="gameover-stat">
            <span className="gameover-stat-label">High Score</span>
            <span className="gameover-stat-value">{highScore}</span>
          </div>
        </div>

        <button
          className="gameover-button"
          onClick={onRestart}
          autoFocus
        >
          Play Again
        </button>

        <p className="gameover-hint">
          Press <kbd>Esc</kbd>, <kbd>Enter</kbd>, or <kbd>Space</kbd> to restart
        </p>
      </div>
    </div>
  );
}
