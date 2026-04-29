import './ScoreDisplay.css';

/**
 * ScoreDisplay — Shows the player's current score, best score,
 * number of moves, and matched pairs progress.
 *
 * Props:
 *  - score       : number — current score
 *  - bestScore   : number — all-time best score
 *  - moves       : number — total moves made
 *  - matchedPairs: number — how many pairs have been found
 *  - totalPairs  : number — total pairs on the board
 */
export default function ScoreDisplay({ score, bestScore, moves, matchedPairs, totalPairs }) {
  return (
    <div className="score-display" role="status" aria-label="Game score">
      <div className="score-display__item score-display__item--score">
        <span className="score-display__label">Score</span>
        <span className="score-display__value">{score}</span>
      </div>

      <div className="score-display__item score-display__item--best">
        <span className="score-display__label">Best</span>
        <span className="score-display__value">{bestScore}</span>
      </div>

      <div className="score-display__item score-display__item--moves">
        <span className="score-display__label">Moves</span>
        <span className="score-display__value">{moves}</span>
      </div>

      <div className="score-display__item score-display__item--progress">
        <span className="score-display__label">Pairs</span>
        <span className="score-display__value">
          {matchedPairs}/{totalPairs}
        </span>
      </div>
    </div>
  );
}
