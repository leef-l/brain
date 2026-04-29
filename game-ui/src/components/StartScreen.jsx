import './StartScreen.css';

/**
 * StartScreen — Welcome screen shown before the game begins.
 * Displays the title, game rules, a difficulty selector, and a start button.
 *
 * Props:
 *  - onStart    : (difficulty) => void — called when player clicks Start
 *  - difficulty : string — current selected difficulty ('easy'|'medium'|'hard')
 *  - onChangeDifficulty : (difficulty) => void
 */
export default function StartScreen({ onStart, difficulty, onChangeDifficulty }) {
  const difficulties = [
    { key: 'easy', label: 'Easy', pairs: 4, desc: '4 pairs · 8 cards' },
    { key: 'medium', label: 'Medium', pairs: 6, desc: '6 pairs · 12 cards' },
    { key: 'hard', label: 'Hard', pairs: 8, desc: '8 pairs · 16 cards' },
  ];

  return (
    <div className="start-screen">
      <div className="start-screen__logo" aria-hidden="true">🧠</div>

      <h1 className="start-screen__title">
        <span>Memory</span> Match
      </h1>

      <p className="start-screen__subtitle">
        Flip cards to find matching pairs.
        <br />
        Match all pairs to win!
      </p>

      <div className="start-screen__difficulty" role="radiogroup" aria-label="Difficulty">
        {difficulties.map((d) => (
          <button
            key={d.key}
            className={`start-screen__difficulty-btn ${
              difficulty === d.key ? 'start-screen__difficulty-btn--active' : ''
            }`}
            role="radio"
            aria-checked={difficulty === d.key}
            onClick={() => onChangeDifficulty(d.key)}
          >
            <span className="start-screen__difficulty-label">{d.label}</span>
            <span className="start-screen__difficulty-desc">{d.desc}</span>
          </button>
        ))}
      </div>

      <button className="start-screen__start-btn" onClick={() => onStart(difficulty)}>
        Start Game
      </button>

      <p className="start-screen__hint">Choose your difficulty and press Start</p>
    </div>
  );
}
