import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

// 设计 token 的唯一实现来源（[09 §4.2]），必须在应用挂载前生效。
import './theme/tokens.css';
import App from './App';

const container = document.getElementById('root');
if (!container) {
  throw new Error('缺少 #root 挂载点');
}

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
