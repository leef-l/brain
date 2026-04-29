import { useEffect } from 'react';
import './GameOverOverlay.css';

/**
 * GameOverOverlay — Full-screen overlay displayed when the game ends.
 * Shows final stats and a play-again button.
 *
 * Props:
 *  - visible      : boolean
 *  - score        : number — final score
 *  - moves        : number — total moves made
 *  - elapsedTime  : number — total seconds elapsed
 *  - totalPairs   : number — total pairs in the game
 *  - isNewBest    : boolean — whether this is a new high score
 *  - onRestart    : () => void — callback when "Play Again" is clicked
 */
export default function GameOverOverlay({
  visible,
  score,
  moves,
  elapsedTime,
  totalPairs,
  isNewBest,
  onRestart,
}) {
  // Trap focus inside the overlay for accessibility
  useEffect(() => {
    if (!visible) return;
    const handleKeyDown = (e) => {
      if (e.key === 'Escape') {
        onRestart?.();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [visible, onRestart]);

  if (!visible) return null;

  const formatTime = (sec) => {
    const m = Math.floor(sec / 60);
    const s = sec % 60;
    return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  };

  return (
    <div className="overlay" role="dialog" aria-modal="true" aria-label="Game over">
      <div className="overlay__backdrop" onClick={onRestart} />
      <div className="overlay__panel">
        <div className="overlay__confetti" aria-hidden="true">
          {Array.from({ length: 12 }).map((_, i) => (
            <span
              key={i}
              className="overlay__confetti-piece"
              style={{
                '--x': `${Math.random() * 100}%`,
                '--delay': `${Math.random() * 2}s`,
                '--hue': Math.floor(Math.random() * 360),
              }}
            />
          ))}
        </div>

        <h2 className="overlay__heading">
          {isNewBest ? '🏆 New High Score!' : '🎉 Well Done!'}
        </h2>

        {isNewBest && (
          <p className="overlay__new-best-badge">Personal Best</p>
        )}

        <div className="overlay__stats">
          <div className="overlay__stat">
            <span className="overlay__stat-label">Final Score</span>
            <span className="overlay__stat-value overlay__stat-value--score">
              {score}
            </span>
          </div>
          <div className="overlay__stat">
            <span className="overlay__stat-label">Total Moves</span>
            <span className="overlay__stat-value">{moves}</span>
          </div>
          <div className="overlay__stat">
            <span className="overlay__stat-label">Time</span>
            <span className="overlay__stat-value">{formatTime(elapsedTime)}</span>
          </div>
          <div className="overlay__stat">
            <span className="overlay__stat-label">Efficiency</span>
            <span className="overlay__stat-value">
              {moves > 0 ? Math.round((totalPairs / moves) * 100) : 0}%
            </span>
          </div>
        </div>

        <button className="overlay__button" onClick={onRestart} autoFocus>
          Play Again
        </button>

        <p className="overlay__hint">
          Press <kbd>Esc</kbd> to restart
        </p>
      </div>
    </div>
  );
}
